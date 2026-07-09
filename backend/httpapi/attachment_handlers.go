package httpapi

import (
	"errors"
	"io"
	"net/http"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/workindex"
)

// attachmentMaxBytes bounds one uploaded attachment (scans and PDFs, not
// media masters).
const attachmentMaxBytes = 20 << 20

// registerAttachments mounts staff work attachments (tasks/229, 058 item
// 2): arbitrary working files stored in the blob store with an editorial
// lcat:attachment statement per file. Librarian-only end to end -- nothing
// here is projected publicly -- and downloads serve as octet-stream
// attachments, so an uploaded HTML file is a download, never a page.
func registerAttachments(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	mux.Handle("GET /v1/works/{id}/attachments", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		grain, _, err := bs.Get(r.Context(), bibframe.GrainPath(workID))
		if errors.Is(err, blob.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such work")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain store unavailable")
			return
		}
		names, err := bibframe.AttachmentsOf(grain, workID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "unreadable grain")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"attachments": names})
	})))

	mux.Handle("POST /v1/works/{id}/attachments", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		name := r.URL.Query().Get("name")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		if !bibframe.ValidAttachmentName(name) {
			writeError(w, http.StatusBadRequest, "name must be a plain filename (letters, digits, dot, dash, underscore)")
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, attachmentMaxBytes))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "attachment too large (20MB cap)")
			return
		}
		if len(data) == 0 {
			writeError(w, http.StatusBadRequest, "empty body")
			return
		}
		// Grain first: the describes-guard means a typo'd id never stores
		// orphan bytes (the tasks/215 covers discipline).
		etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
			return bibframe.SetAttachment(g, workID, name, true)
		})
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if _, err := bs.Put(r.Context(), bibframe.AttachmentBlobPath(workID, name), data, blob.PutOptions{}); err != nil {
			writeError(w, http.StatusInternalServerError, "attachment store failed")
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "ATTACHMENT_ADD", Actor: id.Email, ETag: etag, Note: name,
			})
		}
		writeJSON(w, http.StatusCreated, map[string]string{"workId": workID, "name": name, "etag": etag})
	})))

	mux.Handle("GET /v1/works/{id}/attachments/{name}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workID, name := r.PathValue("id"), r.PathValue("name")
		if !workIDPattern.MatchString(workID) || !bibframe.ValidAttachmentName(name) {
			writeError(w, http.StatusNotFound, "no such attachment")
			return
		}
		data, _, err := bs.Get(r.Context(), bibframe.AttachmentBlobPath(workID, name))
		if err != nil {
			writeError(w, http.StatusNotFound, "no such attachment")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(data)
	})))

	mux.Handle("DELETE /v1/works/{id}/attachments/{name}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID, name := r.PathValue("id"), r.PathValue("name")
		if !workIDPattern.MatchString(workID) || !bibframe.ValidAttachmentName(name) {
			writeError(w, http.StatusBadRequest, "bad work id or name")
			return
		}
		etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
			return bibframe.SetAttachment(g, workID, name, false)
		})
		if err != nil {
			writeMutateError(w, err)
			return
		}
		_ = bs.Delete(r.Context(), bibframe.AttachmentBlobPath(workID, name))
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "ATTACHMENT_REMOVE", Actor: id.Email, ETag: etag, Note: name,
			})
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}
