package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/marcview"
	"github.com/freeeve/libcat/backend/profilesvc"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/backend/workindex"
)

// registerMARC mounts the MARC half of the dual-view editor (tasks/049):
// GET materializes the grain's records as editable field arrays (verbatim
// sidecar fields included, lossy tags annotated); POST writes an edited
// record back as an editorial diff under If-Match, with dryRun returning the
// exact quad delta. The fidelity table rides along so the SPA can warn
// without hardcoding it.
func registerMARC(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, prof *profilesvc.Service, vocabIx *vocab.Index, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	readGrain := func(w http.ResponseWriter, r *http.Request) ([]byte, string, string, bool) {
		return readWorkGrain(w, r, bs)
	}

	mux.Handle("GET /v1/works/{id}/marc", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grain, etag, workID, ok := readGrain(w, r)
		if !ok {
			return
		}
		docs, err := marcview.View(grain)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "marc materialization failed")
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]any{
			"workId": workID, "etag": etag, "records": docs, "knownLoss": bibframe.KnownLoss,
		})
	})))

	// Live preview (tasks/070): the staged native ops applied to the current
	// doc, then encoded as MARC -- nothing written. Empty ops previews the
	// saved state, so the pane snaps back when edits are discarded.
	mux.Handle("POST /v1/works/{id}/marc/preview", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Ops []editor.Op `json:"ops"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if len(req.Ops) > 200 {
			writeError(w, http.StatusBadRequest, "at most 200 ops")
			return
		}
		grain, etag, workID, ok := readGrain(w, r)
		if !ok {
			return
		}
		updated := grain
		if len(req.Ops) > 0 {
			var err error
			updated, err = editor.ApplyOps(prof.Mapper(), grain, workID, req.Ops, vocabIx.LabelResolver())
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		docs, err := marcview.View(updated)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "marc materialization failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"workId": workID, "etag": etag, "records": docs, "knownLoss": bibframe.KnownLoss,
		})
	})))

	mux.Handle("POST /v1/works/{id}/marc", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Index  int                `json:"index"`
			Record marcview.RecordDoc `json:"record"`
			DryRun bool               `json:"dryRun"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
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
		updated, err := marcview.Save(grain, req.Index, req.Record)
		if errors.Is(err, marcview.ErrValidation) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "marc save failed")
			return
		}
		diff := editor.DiffLines(grain, updated)
		if req.DryRun {
			writeJSON(w, http.StatusOK, map[string]any{"etag": etag, "diff": diff})
			return
		}
		if len(diff.Added) == 0 && len(diff.Removed) == 0 {
			// Untouched save: nothing written, same token.
			w.Header().Set("ETag", etag)
			writeJSON(w, http.StatusOK, map[string]any{"workId": workID, "etag": etag, "diff": diff})
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
				WorkID: workID, Action: "MARC_EDIT", Actor: id.Email, ETag: newTag,
			})
		}
		w.Header().Set("ETag", newTag)
		writeJSON(w, http.StatusOK, map[string]any{"workId": workID, "etag": newTag, "diff": diff})
	})))
}
