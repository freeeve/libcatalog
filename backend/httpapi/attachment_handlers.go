package httpapi

import (
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"slices"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/workindex"
)

// attachmentMaxBytes bounds one uploaded attachment (scans and PDFs, not
// media masters).
const attachmentMaxBytes = 20 << 20

// registerAttachments mounts staff work attachments (058 item
// 2): arbitrary working files stored in the blob store with an editorial
// lcat:attachment statement per file. Librarian-only end to end -- nothing
// here is projected publicly -- and downloads serve as octet-stream
// attachments, so an uploaded HTML file is a download, never a page.
func registerAttachments(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, verifier auth.TokenVerifier, logger *slog.Logger) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	// A half-completed attachment write is the one thing here worth an
	// operator's attention, and it is reported from four places below.
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

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
			writeError(w, http.StatusBadRequest, "name must be a filename: no slashes or control characters, 100 bytes or fewer")
			return
		}
		path, err := bibframe.AttachmentBlobPath(workID, name)
		if err != nil {
			writeError(w, http.StatusBadRequest, "unusable attachment name")
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
		held, err := attachmentNames(r, bs, workID)
		if err != nil {
			writeMutateError(w, err)
			return
		}
		// Whether the record already claimed this name decides what a failed
		// byte write must undo, below.
		existed := slices.Contains(held, name)
		// An upload never lands on another file's bytes. Replacing
		// is a deliberate act, not the default a second POST falls into.
		if existed && r.URL.Query().Get("replace") != "true" {
			writeError(w, http.StatusConflict, name+" is already attached; delete it first, or POST with ?replace=true")
			return
		}
		// Grain first: the describes-guard means a typo'd id never stores
		// orphan bytes (the covers discipline). The cost is that a
		// failed byte write leaves a statement the bytes do not back, so the
		// grain write is compensated below rather than abandoned.
		etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
			return bibframe.SetAttachment(g, workID, name, true)
		})
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if _, err := bs.Put(r.Context(), path, data, blob.PutOptions{}); err != nil {
			// Undo only what this request added. On ?replace=true the statement
			// predates us and the previous bytes are still stored, so removing
			// it would delete a working attachment to report a failed one.
			if !existed {
				if _, rerr := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
					return bibframe.SetAttachment(g, workID, name, false)
				}); rerr != nil {
					logger.Error("attachment rollback failed: the record claims an attachment whose bytes were never stored",
						"workId", workID, "name", name, "actor", id.Email, "put", err, "rollback", rerr)
					writeError(w, http.StatusInternalServerError,
						"the attachment was recorded but its bytes were not stored, and the record could not be rolled back: delete "+name+" and retry")
					return
				}
			}
			logger.Error("attachment bytes were not stored", "workId", workID, "name", name, "actor", id.Email, "err", err)
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
		data, err := readAttachment(r, bs, workID, name)
		if err != nil {
			writeError(w, http.StatusNotFound, "no such attachment")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", contentDisposition(name))
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
		// 204 promises the bytes are gone. When they are not, restore the
		// statement rather than orphan a file the grain no longer indexes:
		// staff attachments are the private side of a record, and a librarian
		// deleting one for a legal reason must not be told it worked.
		if err := deleteAttachmentBytes(r, bs, workID, name); err != nil {
			if _, rerr := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
				return bibframe.SetAttachment(g, workID, name, true)
			}); rerr != nil {
				logger.Error("attachment bytes survived a delete and the record could not be restored: the bytes are now orphaned",
					"workId", workID, "name", name, "actor", id.Email, "delete", err, "restore", rerr)
				writeError(w, http.StatusInternalServerError,
					"the attachment's bytes could not be removed and the record could not be restored: "+name+" is no longer listed but its bytes remain")
				return
			}
			logger.Error("attachment bytes could not be removed", "workId", workID, "name", name, "actor", id.Email, "err", err)
			writeError(w, http.StatusInternalServerError, "attachment bytes could not be removed; the record is unchanged")
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "ATTACHMENT_REMOVE", Actor: id.Email, ETag: etag, Note: name,
			})
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}

// attachmentNames reads the work's current attachment display names, mapping
// a missing grain onto the shared mutate errors so the caller reports 404 and
// 500 the same way every other work route does.
// deleteAttachmentBytes removes an attachment's bytes from both the current
// path and the legacy one, and reports the first error that was not "already
// gone". A missing object is the state a delete is asking for, so ErrNotFound
// is success -- the legacy path in particular is absent for every record
// written since the layout changed.
func deleteAttachmentBytes(r *http.Request, bs blob.Store, workID, name string) error {
	path, err := bibframe.AttachmentBlobPath(workID, name)
	if err != nil {
		return err
	}
	paths := []string{path}
	if legacy := bibframe.LegacyAttachmentBlobPath(workID, name); legacy != "" {
		paths = append(paths, legacy)
	}
	for _, p := range paths {
		if err := bs.Delete(r.Context(), p); err != nil && !errors.Is(err, blob.ErrNotFound) {
			return err
		}
	}
	return nil
}

func attachmentNames(r *http.Request, bs blob.Store, workID string) ([]string, error) {
	grain, _, err := bs.Get(r.Context(), bibframe.GrainPath(workID))
	if errors.Is(err, blob.ErrNotFound) {
		return nil, errWorkNotFound
	}
	if err != nil {
		return nil, errGrainStore
	}
	return bibframe.AttachmentsOf(grain, workID)
}

// readAttachment fetches an attachment's bytes, falling back to the pre-236
// path where the display name was the blob segment, so changing the encoding
// did not orphan files already stored.
func readAttachment(r *http.Request, bs blob.Store, workID, name string) ([]byte, error) {
	path, err := bibframe.AttachmentBlobPath(workID, name)
	if err != nil {
		return nil, err
	}
	data, _, err := bs.Get(r.Context(), path)
	if err == nil {
		return data, nil
	}
	legacy := bibframe.LegacyAttachmentBlobPath(workID, name)
	if legacy == "" {
		return nil, err
	}
	data, _, err = bs.Get(r.Context(), legacy)
	return data, err
}

// contentDisposition renders the download filename safely for any script.
// mime.FormatMediaType RFC 2231-encodes what a quoted string cannot carry, so
// a filename with a quote or a CJK stem can neither break the header nor
// inject one.
func contentDisposition(name string) string {
	if v := mime.FormatMediaType("attachment", map[string]string{"filename": name}); v != "" {
		return v
	}
	return "attachment"
}
