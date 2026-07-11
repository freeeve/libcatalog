package httpapi

import (
	"errors"
	"net/http"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/workindex"
)

// registerClone mounts the clone-work endpoint: POST
// copies a work into a brand-new editorial-only grain -- fresh work/instance
// ids, provider keys gone, born suppressed -- and returns the new id for the
// editor to open. This is the first eager create-work path: every other
// surface births works through the ingest pipeline.
func registerClone(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	mux.Handle("POST /v1/works/{id}/clone", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		src, _, err := bs.Get(r.Context(), bibframe.GrainPath(workID))
		if errors.Is(err, blob.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such work")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain store unavailable")
			return
		}
		cloned, newID, err := bibframe.CloneGrain(src, workID)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		path := bibframe.GrainPath(newID)
		newTag, err := bs.Put(r.Context(), path, cloned, blob.PutOptions{IfNoneMatch: true, ContentType: "application/n-quads"})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "clone store failed")
			return
		}
		ix.Apply(path, newTag, cloned)
		_ = ix.AppendFeed(r.Context(), path)
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: newID, Action: "WORK_CLONE", Actor: id.Email, ETag: newTag, Note: "cloned from " + workID,
			})
		}
		writeJSON(w, http.StatusCreated, map[string]string{"workId": newID, "from": workID, "etag": newTag})
	})))
}
