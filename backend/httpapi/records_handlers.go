package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/identity"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/editor"
	"github.com/freeeve/libcatalog/backend/profilesvc"
	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/suggest"
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
func registerRecords(mux *http.ServeMux, bs blob.Store, db store.Store, queue *suggest.Service, prof *profilesvc.Service, verifier auth.TokenVerifier, hook WorkSaveHook) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	readGrain := func(w http.ResponseWriter, r *http.Request) ([]byte, string, string, bool) {
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return nil, "", "", false
		}
		grain, etag, err := bs.Get(r.Context(), bibframe.GrainPath(workID))
		if errors.Is(err, blob.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such work")
			return nil, "", "", false
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain read failed")
			return nil, "", "", false
		}
		return grain, etag, workID, true
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
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]any{"etag": etag, "doc": doc})
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
		updated, err := editor.ApplyOps(prof.Mapper(), grain, workID, req.Ops)
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
			if dup := findDuplicate(r.Context(), bs, workID, updated); dup != nil {
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
		if dup := findDuplicate(r.Context(), bs, workID, updated); dup != nil {
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
		grain, etag, _, ok := readGrain(w, r)
		if !ok {
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
		// The marker lives in the survivor's grain (tasks/001 semantics).
		etag, err := mutateWorkGrain(r, bs, req.To, func(grain []byte) ([]byte, error) {
			return bibframe.AddMergeMarker(grain, req.From, req.To)
		})
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
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
		etag, err := mutateWorkGrain(r, bs, req.From, func(grain []byte) ([]byte, error) {
			return bibframe.AddSplitMarkers(grain, newWork, req.From, req.Instances)
		})
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
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
			if req.DryRun {
				grain, _, err := bs.Get(r.Context(), bibframe.GrainPath(workID))
				if err != nil {
					results = append(results, itemResult{WorkID: workID, Error: "no such work"})
					continue
				}
				diff, _, err := editor.ComputeDiff(grain, req.Patch)
				if err != nil {
					results = append(results, itemResult{WorkID: workID, Error: err.Error()})
					continue
				}
				results = append(results, itemResult{WorkID: workID, Diff: &diff})
				continue
			}
			etag, err := mutateWorkGrain(r, bs, workID, func(grain []byte) ([]byte, error) {
				return bibframe.ApplyEditorialPatch(grain, req.Patch.ToBibframe())
			})
			if err != nil {
				results = append(results, itemResult{WorkID: workID, Error: err.Error()})
				continue
			}
			results = append(results, itemResult{WorkID: workID, ETag: etag})
		}
		if !req.DryRun && queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				Action: "BATCH_EDIT", Actor: id.Email,
				Note: batchNote(results),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	})))

	registerDrafts(mux, db, librarian)
}

// mutateWorkGrain CAS-updates one work's grain, retrying from fresh (server-
// initiated edits like merge/split/batch own their concurrency, unlike the
// client-token PUT).
func mutateWorkGrain(r *http.Request, bs blob.Store, workID string, mutate func([]byte) ([]byte, error)) (string, error) {
	path := bibframe.GrainPath(workID)
	for attempt := range 6 {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt*attempt) * 5 * time.Millisecond)
		}
		grain, etag, err := bs.Get(r.Context(), path)
		if err != nil {
			return "", errors.New("no such work")
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
			return "", err
		}
		return newTag, nil
	}
	return "", errors.New("write kept conflicting")
}

func batchNote(results any) string {
	b, _ := json.Marshal(results)
	if len(b) > 512 {
		b = b[:512]
	}
	return string(b)
}
