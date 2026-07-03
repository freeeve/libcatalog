package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/editor"
	"github.com/freeeve/libcatalog/backend/marcview"
	"github.com/freeeve/libcatalog/backend/suggest"
)

// registerMARC mounts the MARC half of the dual-view editor (tasks/049):
// GET materializes the grain's records as editable field arrays (verbatim
// sidecar fields included, lossy tags annotated); POST writes an edited
// record back as an editorial diff under If-Match, with dryRun returning the
// exact quad delta. The fidelity table rides along so the SPA can warn
// without hardcoding it.
func registerMARC(mux *http.ServeMux, bs blob.Store, queue *suggest.Service, verifier auth.TokenVerifier) {
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
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "MARC_EDIT", Actor: id.Email, ETag: newTag,
			})
		}
		w.Header().Set("ETag", newTag)
		writeJSON(w, http.StatusOK, map[string]any{"workId": workID, "etag": newTag, "diff": diff})
	})))
}
