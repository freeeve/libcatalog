package httpapi

import (
	"io"
	"net/http"
	"strings"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/workindex"
)

// coverMaxBytes bounds an uploaded cover; typical covers are well under 1MB.
const coverMaxBytes = 2 << 20

// coverTypes maps accepted upload content types to blob extensions.
var coverTypes = map[string]string{
	"image/jpeg": "jpg",
	"image/png":  "png",
	"image/webp": "webp",
}

// registerCovers mounts per-work cover art (tasks/215, 058 item 2): PUT
// stores the image bytes in the blob store and records the editorial
// lcat:extra/cover URL the OPAC's cover slot already reads (tasks/022/025);
// DELETE removes both. GET serves the bytes publicly -- covers are display
// assets the static site republishes anyway.
func registerCovers(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	mux.Handle("PUT /v1/works/{id}/cover", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		ext, ok := coverTypes[strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])]
		if !ok {
			writeError(w, http.StatusUnsupportedMediaType, "cover must be image/jpeg, image/png, or image/webp")
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, coverMaxBytes))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "cover too large (2MB cap)")
			return
		}
		if len(data) == 0 {
			writeError(w, http.StatusBadRequest, "empty body")
			return
		}
		url := "covers/" + workID + "." + ext
		// Grain first: SetCover verifies the work exists, so a typo'd id
		// never stores orphan bytes.
		etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
			return bibframe.SetCover(g, workID, url)
		})
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if _, err := bs.Put(r.Context(), bibframe.CoverBlobPath(workID, ext), data, blob.PutOptions{}); err != nil {
			writeError(w, http.StatusInternalServerError, "cover store failed")
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "COVER_SET", Actor: id.Email, ETag: etag, Note: url,
			})
		}
		writeJSON(w, http.StatusOK, map[string]string{"workId": workID, "cover": url, "etag": etag})
	})))

	mux.Handle("DELETE /v1/works/{id}/cover", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
			return bibframe.SetCover(g, workID, "")
		})
		if err != nil {
			writeMutateError(w, err)
			return
		}
		for ext := range map[string]bool{"jpg": true, "png": true, "webp": true} {
			_ = bs.Delete(r.Context(), bibframe.CoverBlobPath(workID, ext))
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "COVER_REMOVE", Actor: id.Email, ETag: etag,
			})
		}
		w.WriteHeader(http.StatusNoContent)
	})))

	// Public read: the admin SPA and any preview render from here; the
	// static site ships its own copies (lcat export -covers).
	mux.HandleFunc("GET /covers/{file}", func(w http.ResponseWriter, r *http.Request) {
		file := r.PathValue("file")
		dot := strings.LastIndexByte(file, '.')
		if dot < 0 {
			writeError(w, http.StatusNotFound, "no such cover")
			return
		}
		workID, ext := file[:dot], file[dot+1:]
		ct := ""
		for typ, e := range coverTypes {
			if e == ext {
				ct = typ
			}
		}
		if ct == "" || !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusNotFound, "no such cover")
			return
		}
		data, _, err := bs.Get(r.Context(), bibframe.CoverBlobPath(workID, ext))
		if err != nil {
			writeError(w, http.StatusNotFound, "no such cover")
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_, _ = w.Write(data)
	})
}
