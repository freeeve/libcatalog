package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/profilesvc"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/backend/workindex"
)

// grainView is the read shape of a record: raw canonical N-Quads plus the
// concurrency token. The typed WorkDoc rides on top in tasks/041.
type grainView struct {
	WorkID string `json:"workId"`
	ETag   string `json:"etag"`
	NQuads string `json:"nquads"`
}

// WorkSaveHook runs after a successful record write -- the seam the authority
// auto-linker plugs into (tasks/046). Hook failures never fail the save; the
// moderation queue is best-effort from the editor's perspective.
type WorkSaveHook interface {
	AutoLink(ctx context.Context, workID string, grain []byte) (int, error)
}

// registerRecords mounts the librarian record-editing surface: grain
// read/write with ETag optimistic locking, dry-run validation, drafts,
// merge/split, and quad-level batch edits.
func registerRecords(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, db store.Store, queue *suggest.Service, prof *profilesvc.Service, vocabIx *vocab.Index, verifier auth.TokenVerifier, hook WorkSaveHook) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	readGrain := func(w http.ResponseWriter, r *http.Request) ([]byte, string, string, bool) {
		return readWorkGrain(w, r, bs)
	}

	mux.Handle("GET /v1/works/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grain, etag, workID, ok := readGrain(w, r)
		if !ok {
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, grainView{WorkID: workID, ETag: etag, NQuads: string(grain)})
	})))

	// The typed editing document: the grain materialized through the live
	// profile set, so a runtime profile edit shows in the editor at once.
	mux.Handle("GET /v1/works/{id}/doc", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grain, etag, workID, ok := readGrain(w, r)
		if !ok {
			return
		}
		doc, err := prof.Mapper().ToDoc(grain, workID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "doc mapping failed")
			return
		}
		// The cover is not a profile field, so it is not in doc.work.fields.
		// The editor's Cover panel needs it at load time to show what the record
		// has and to offer Remove (tasks/242).
		cover, err := bibframe.CoverOf(grain, workID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "doc mapping failed")
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]any{"etag": etag, "doc": doc, "cover": cover})
	})))

	// PUT applies an editorial patch under the client's If-Match token. No
	// silent retry: a concurrent write returns 412 with the fresh state so
	// the client can rebase deliberately.
	mux.Handle("PUT /v1/works/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		ifMatch := r.Header.Get("If-Match")
		if ifMatch == "" {
			writeError(w, http.StatusPreconditionRequired, "If-Match required")
			return
		}
		var patch editor.Patch
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if err := patch.Validate(nil); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		grain, etag, workID, ok := readGrain(w, r)
		if !ok {
			return
		}
		if err := patch.BoundTo(workID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if etag != ifMatch {
			w.Header().Set("ETag", etag)
			writeJSON(w, http.StatusPreconditionFailed, grainView{WorkID: workID, ETag: etag, NQuads: string(grain)})
			return
		}
		updated, err := bibframe.ApplyEditorialPatch(grain, patch.ToBibframe())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		newTag, err := bs.Put(r.Context(), bibframe.GrainPath(workID), updated, blob.PutOptions{
			IfMatch: etag, ContentType: "application/n-quads",
		})
		if errors.Is(err, blob.ErrPreconditionFailed) {
			fresh, freshTag, _, ok := readGrain(w, r)
			if !ok {
				return
			}
			w.Header().Set("ETag", freshTag)
			writeJSON(w, http.StatusPreconditionFailed, grainView{WorkID: workID, ETag: freshTag, NQuads: string(fresh)})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain write failed")
			return
		}
		ix.Apply(bibframe.GrainPath(workID), newTag, updated)
		// Publish the change to the feed so other containers read-their-writes
		// without a corpus List; best-effort, the refresh backstop covers it.
		_ = ix.AppendFeed(r.Context(), bibframe.GrainPath(workID))
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "RECORD_EDIT", Actor: id.Email, ETag: newTag,
			})
		}
		if hook != nil {
			_, _ = hook.AutoLink(r.Context(), workID, updated)
		}
		w.Header().Set("ETag", newTag)
		writeJSON(w, http.StatusOK, map[string]string{"workId": workID, "etag": newTag})
	})))

	// Field-level operations: the SPA's write path (tasks/045). Ops apply
	// through the profile mapper with the tasks/042 override semantics;
	// dryRun returns the exact quad delta without writing.
	mux.Handle("POST /v1/works/{id}/ops", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Ops    []editor.Op `json:"ops"`
			DryRun bool        `json:"dryRun"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if len(req.Ops) == 0 || len(req.Ops) > 200 {
			writeError(w, http.StatusBadRequest, "1-200 ops per request")
			return
		}
		ifMatch := r.Header.Get("If-Match")
		if !req.DryRun && ifMatch == "" {
			writeError(w, http.StatusPreconditionRequired, "If-Match required")
			return
		}
		grain, etag, workID, ok := readGrain(w, r)
		if !ok {
			return
		}
		if !req.DryRun && etag != ifMatch {
			w.Header().Set("ETag", etag)
			writeJSON(w, http.StatusPreconditionFailed, grainView{WorkID: workID, ETag: etag, NQuads: string(grain)})
			return
		}
		updated, err := editor.ApplyOps(prof.Mapper(), grain, workID, req.Ops, vocabIx.LabelResolver())
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		diff := editor.DiffLines(grain, updated)
		if req.DryRun {
			resp := map[string]any{"etag": etag, "diff": diff}
			// The materialized post-edit doc lets a client render the result
			// without persisting -- the read-only/sandbox demo's "save" that
			// shows the change but never writes.
			if doc, derr := prof.Mapper().ToDoc(updated, workID); derr == nil {
				resp["doc"] = doc
			}
			if dup := findDuplicate(r.Context(), ix, workID, updated); dup != nil {
				resp["duplicate"] = dup
			}
			writeJSON(w, http.StatusOK, resp)
			return
		}
		newTag, err := bs.Put(r.Context(), bibframe.GrainPath(workID), updated, blob.PutOptions{
			IfMatch: etag, ContentType: "application/n-quads",
		})
		if errors.Is(err, blob.ErrPreconditionFailed) {
			fresh, freshTag, _, ok := readGrain(w, r)
			if !ok {
				return
			}
			w.Header().Set("ETag", freshTag)
			writeJSON(w, http.StatusPreconditionFailed, grainView{WorkID: workID, ETag: freshTag, NQuads: string(fresh)})
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain write failed")
			return
		}
		ix.Apply(bibframe.GrainPath(workID), newTag, updated)
		// Publish the change to the feed so other containers read-their-writes
		// without a corpus List; best-effort, the refresh backstop covers it.
		_ = ix.AppendFeed(r.Context(), bibframe.GrainPath(workID))
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "RECORD_EDIT", Actor: id.Email, ETag: newTag,
				Note: fmt.Sprintf("%d ops", len(req.Ops)),
			})
		}
		if hook != nil {
			_, _ = hook.AutoLink(r.Context(), workID, updated)
		}
		w.Header().Set("ETag", newTag)
		resp := map[string]any{"workId": workID, "etag": newTag, "diff": diff}
		if dup := findDuplicate(r.Context(), ix, workID, updated); dup != nil {
			resp["duplicate"] = dup
		}
		writeJSON(w, http.StatusOK, resp)
	})))

	// Dry-run: the exact quad delta the patch would make, nothing written.
	mux.Handle("POST /v1/works/{id}/validate", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var patch editor.Patch
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&patch); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if err := patch.Validate(nil); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		grain, etag, workID, ok := readGrain(w, r)
		if !ok {
			return
		}
		// The preview refuses exactly what the write refuses, or it previews
		// a request that cannot succeed.
		if err := patch.BoundTo(workID); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		diff, _, err := editor.ComputeDiff(grain, patch)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"etag": etag, "diff": diff})
	})))

	mux.Handle("POST /v1/works/merge", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct{ From, To string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			!workIDPattern.MatchString(req.From) || !workIDPattern.MatchString(req.To) || req.From == req.To {
			writeError(w, http.StatusBadRequest, "merge needs distinct from and to work ids")
			return
		}
		// The retiring work must exist: the marker is a permanent
		// instruction to the identity resolver (SeedMerge, with no removal
		// route), so a mistyped from used to record false provenance
		// against a work that was never there (tasks/214). The survivor's
		// existence is checked by mutateWorkGrain reading its grain.
		if _, _, err := bs.Get(r.Context(), bibframe.GrainPath(req.From)); err != nil {
			writeError(w, http.StatusNotFound, "no such work: "+req.From)
			return
		}
		// The marker lives in the survivor's grain (tasks/001 semantics).
		etag, err := mutateWorkGrain(r, bs, ix, req.To, func(grain []byte) ([]byte, error) {
			return bibframe.AddMergeMarker(grain, req.From, req.To)
		})
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: req.To, Action: "MERGE", Actor: id.Email,
				Note: "merged " + req.From + " into " + req.To, ETag: etag,
			})
		}
		writeJSON(w, http.StatusOK, map[string]string{"from": req.From, "to": req.To, "etag": etag})
	})))

	mux.Handle("POST /v1/works/split", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			From      string   `json:"from"`
			Instances []string `json:"instances"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			!workIDPattern.MatchString(req.From) || len(req.Instances) == 0 {
			writeError(w, http.StatusBadRequest, "split needs a source work and instance ids")
			return
		}
		newWork := identity.Mint(identity.WorkPrefix)
		etag, err := mutateWorkGrain(r, bs, ix, req.From, func(grain []byte) ([]byte, error) {
			return bibframe.AddSplitMarkers(grain, newWork, req.From, req.Instances)
		})
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: req.From, Action: "SPLIT", Actor: id.Email,
				Note: "split " + newWork + " from " + req.From, ETag: etag,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"from": req.From, "newWork": newWork, "instances": req.Instances, "etag": etag})
	})))

	// Batch: one patch applied to many works, per-work results.
	mux.Handle("POST /v1/batch", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			WorkIDs []string     `json:"workIds"`
			Patch   editor.Patch `json:"patch"`
			DryRun  bool         `json:"dryRun"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if len(req.WorkIDs) == 0 || len(req.WorkIDs) > 500 {
			writeError(w, http.StatusBadRequest, "1-500 work ids per batch")
			return
		}
		if err := req.Patch.Validate(nil); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// One patch, many works: its subject names a single Work node, so
		// applied verbatim it would describe that one work inside every other
		// work's grain -- with the dry run agreeing, because it diffed the same
		// verbatim patch (tasks/240). Rebind the subject per work, and refuse
		// outright the patches that cannot be rebound.
		if err := req.Patch.Rebindable(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		type itemResult struct {
			WorkID string       `json:"workId"`
			ETag   string       `json:"etag,omitempty"`
			Diff   *editor.Diff `json:"diff,omitempty"`
			Error  string       `json:"error,omitempty"`
		}
		results := make([]itemResult, 0, len(req.WorkIDs))
		for _, workID := range req.WorkIDs {
			if !workIDPattern.MatchString(workID) {
				results = append(results, itemResult{WorkID: workID, Error: "bad work id"})
				continue
			}
			patch := req.Patch.RebindWork(workID)
			if req.DryRun {
				grain, _, err := bs.Get(r.Context(), bibframe.GrainPath(workID))
				if err != nil {
					results = append(results, itemResult{WorkID: workID, Error: "no such work"})
					continue
				}
				diff, _, err := editor.ComputeDiff(grain, patch)
				if err != nil {
					results = append(results, itemResult{WorkID: workID, Error: err.Error()})
					continue
				}
				results = append(results, itemResult{WorkID: workID, Diff: &diff})
				continue
			}
			etag, err := mutateWorkGrain(r, bs, ix, workID, func(grain []byte) ([]byte, error) {
				return bibframe.ApplyEditorialPatch(grain, patch.ToBibframe())
			})
			if err != nil {
				results = append(results, itemResult{WorkID: workID, Error: err.Error()})
				continue
			}
			results = append(results, itemResult{WorkID: workID, ETag: etag})
		}
		if !req.DryRun && queue != nil {
			// One entry per rewritten record, so a bulk edit is visible in the
			// History tab of each record it changed, plus one aggregate entry
			// for the run (tasks/239). The old aggregate carried a JSON blob of
			// the results cut at 512 bytes -- past ~7 works the cut landed
			// mid-token, and a byte-boundary slice can split a UTF-8 rune.
			runID := suggest.NewRunID()
			note := fmt.Sprintf("quad patch, +%d/-%d statements", len(req.Patch.Add), len(req.Patch.Remove))
			failed := 0
			rewritten := make([]string, 0, len(results))
			for _, res := range results {
				if res.Error != "" {
					failed++
					continue
				}
				rewritten = append(rewritten, res.WorkID)
				queue.WriteAudit(r.Context(), suggest.AuditEntry{
					WorkID: res.WorkID, Action: "BATCH_EDIT", Actor: id.Email,
					ETag: res.ETag, RunID: runID, Note: note,
				})
			}
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				Action: "BATCH_EDIT", Actor: id.Email, RunID: runID,
				Note: suggest.RunNote{
					Selection: "ids", Matched: len(req.WorkIDs), Applied: len(rewritten),
					Rewritten: len(rewritten), Failed: failed,
					Added: len(req.Patch.Add) * len(rewritten), Removed: len(req.Patch.Remove) * len(rewritten),
					Works: rewritten,
				}.String(),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})))

	registerDrafts(mux, db, librarian)
}

// errWorkNotFound and errGrainStore let handlers map a mutateWorkGrain
// failure onto the right status instead of calling everything a conflict
// (tasks/115): missing work 404 (matching the read paths), store fault 500,
// domain error from the mutate func 409.
var (
	errWorkNotFound = errors.New("no such work")
	errGrainStore   = errors.New("grain store unavailable")
)

// writeMutateError maps a mutateWorkGrain failure onto its status code.
func writeMutateError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errWorkNotFound):
		writeError(w, http.StatusNotFound, "no such work")
	case errors.Is(err, bibframe.ErrNoSuchInstance):
		writeError(w, http.StatusBadRequest, "no such instance on this work")
	case errors.Is(err, errGrainStore):
		writeError(w, http.StatusInternalServerError, "grain store unavailable")
	default:
		writeError(w, http.StatusConflict, err.Error())
	}
}

// mutateWorkGrain CAS-updates one work's grain, retrying from fresh (server-
// initiated edits like merge/split/batch own their concurrency, unlike the
// client-token PUT), and pushes the written grain into the shared work index.
func mutateWorkGrain(r *http.Request, bs blob.Store, ix *workindex.Index, workID string, mutate func([]byte) ([]byte, error)) (string, error) {
	path := bibframe.GrainPath(workID)
	for attempt := range 6 {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * 5 * time.Millisecond)
		}
		grain, etag, err := bs.Get(r.Context(), path)
		if err != nil {
			if errors.Is(err, blob.ErrNotFound) {
				return "", errWorkNotFound
			}
			return "", fmt.Errorf("%w: %v", errGrainStore, err)
		}
		updated, err := mutate(grain)
		if err != nil {
			return "", err
		}
		newTag, err := bs.Put(r.Context(), path, updated, blob.PutOptions{IfMatch: etag, ContentType: "application/n-quads"})
		if errors.Is(err, blob.ErrPreconditionFailed) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("%w: %v", errGrainStore, err)
		}
		ix.Apply(path, newTag, updated)
		_ = ix.AppendFeed(r.Context(), path)
		return newTag, nil
	}
	return "", errors.New("write kept conflicting")
}
