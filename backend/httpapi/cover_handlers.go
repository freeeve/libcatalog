package httpapi

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
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

// coverContentType reads the declared upload type. RFC 9110 §8.3.1 makes type
// and subtype case-insensitive, so "Image/PNG" is a correct spelling of
// "image/png" and was being refused with a 415 (tasks/243).
func coverContentType(header string) (ext string, ok bool) {
	declared := strings.ToLower(strings.TrimSpace(strings.Split(header, ";")[0]))
	ext, ok = coverTypes[declared]
	return ext, ok
}

// sniffCover reports the image type of data as its magic bytes declare it,
// which is "" for anything that is not one of the three cover formats. The
// bytes decide, not the request header: a header alone let an HTML document be
// stored and served as image/png, and let a JPEG be stored at a .png path
// (tasks/243).
func sniffCover(data []byte) string {
	sniffed := http.DetectContentType(data)
	if _, ok := coverTypes[sniffed]; ok {
		return sniffed
	}
	return ""
}

// sweepStaleCovers deletes a work's cover blobs in every format except the one
// just written, and reports the first deletion that failed for a reason other
// than the blob already being absent.
//
// Replacing a JPEG with a PNG repointed the grain and left the JPEG serving
// from its public, unauthenticated, guessable URL forever -- nothing referenced
// it, so nothing would ever collect it. A cataloger replaces a cover precisely
// when the old one is wrong: wrong edition, rights complaint, an image that
// should not have been published. A takedown that looks done was not done
// (tasks/243).
//
// The error was discarded until tasks/266, which made a failing store reproduce
// that exact outcome: the caller answered 2xx while the image kept serving. A
// missing blob stays success -- a cover exists in at most one format, so two of
// these three deletes normally find nothing, and absent is the state a delete
// asks for.
//
// Called only after the new bytes are stored, so a failed write never destroys
// the cover it was replacing.
func sweepStaleCovers(r *http.Request, bs blob.Store, workID, keep string) error {
	for _, ext := range coverExts {
		if ext == keep {
			continue
		}
		err := bs.Delete(r.Context(), bibframe.CoverBlobPath(workID, ext))
		if err != nil && !errors.Is(err, blob.ErrNotFound) {
			return fmt.Errorf("cover %s: %w", ext, err)
		}
	}
	return nil
}

// restoreCover puts a work's cover statement back to url, compensating a byte
// operation that failed after the grain was already written.
func restoreCover(r *http.Request, bs blob.Store, ix *workindex.Index, workID, url string) error {
	_, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
		return bibframe.SetCover(g, workID, url)
	})
	return err
}

// coverExts is every extension a cover may be stored under.
var coverExts = []string{"jpg", "png", "webp"}

// registerCovers mounts per-work cover art (tasks/215, 058 item 2): PUT
// stores the image bytes in the blob store and records the editorial
// lcat:extra/cover URL the OPAC's cover slot already reads (tasks/022/025);
// DELETE removes both. GET serves the bytes publicly -- covers are display
// assets the static site republishes anyway.
func registerCovers(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, verifier auth.TokenVerifier, logger *slog.Logger) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	// A half-completed cover write is what an operator needs to see: the
	// response tells the cataloger, the log tells whoever is on call.
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	mux.Handle("PUT /v1/works/{id}/cover", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		declared := r.Header.Get("Content-Type")
		ext, ok := coverContentType(declared)
		if !ok {
			writeError(w, http.StatusUnsupportedMediaType,
				"cover must be image/jpeg, image/png, or image/webp; got "+strconv.Quote(declared))
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
		// The bytes must be the image the header claims, or the blob's declared
		// type is a lie to the OPAC and to `lcat export -covers`.
		switch sniffed := sniffCover(data); {
		case sniffed == "":
			writeError(w, http.StatusBadRequest, "body is not a jpeg, png, or webp image")
			return
		case coverTypes[sniffed] != ext:
			writeError(w, http.StatusBadRequest, "body is "+sniffed+", not the declared "+strings.ToLower(strings.TrimSpace(strings.Split(declared, ";")[0])))
			return
		}
		url := "covers/" + workID + "." + ext
		// Grain first: SetCover verifies the work exists, so a typo'd id
		// never stores orphan bytes. The cost is that a failed byte write
		// leaves a statement the bytes do not back, so it is compensated
		// below rather than abandoned (tasks/266). previous is the cover this
		// request replaces -- what the compensation must restore.
		var previous string
		etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
			cur, err := bibframe.CoverOf(g, workID)
			if err != nil {
				return nil, err
			}
			previous = cur
			return bibframe.SetCover(g, workID, url)
		})
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if _, err := bs.Put(r.Context(), bibframe.CoverBlobPath(workID, ext), data, blob.PutOptions{}); err != nil {
			// Restore the cover this request replaced, not "": on a replacement
			// the previous cover's bytes are still stored and still serving, and
			// clearing the statement would orphan a working public image.
			if rerr := restoreCover(r, bs, ix, workID, previous); rerr != nil {
				logger.Error("cover rollback failed: the record claims a cover whose bytes were never stored",
					"workId", workID, "cover", url, "actor", id.Email, "put", err, "rollback", rerr)
				writeError(w, http.StatusInternalServerError,
					"the cover was recorded but its bytes were not stored, and the record could not be rolled back: remove the cover and retry")
				return
			}
			logger.Error("cover bytes were not stored", "workId", workID, "cover", url, "actor", id.Email, "err", err)
			writeError(w, http.StatusInternalServerError, "cover store failed")
			return
		}
		// The new cover is stored and recorded, but a surviving blob in another
		// format still serves from its own public URL. That is the takedown
		// failure tasks/243 named, so it is not a 200. The upload is idempotent:
		// retrying re-runs the sweep once the store recovers.
		if err := sweepStaleCovers(r, bs, workID, ext); err != nil {
			logger.Error("the replaced cover's bytes could not be removed and are still public",
				"workId", workID, "cover", url, "actor", id.Email, "err", err)
			writeError(w, http.StatusInternalServerError,
				"the new cover was stored, but the cover it replaced could not be removed and is still being served: retry")
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
		var previous string
		etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
			cur, err := bibframe.CoverOf(g, workID)
			if err != nil {
				return nil, err
			}
			previous = cur
			return bibframe.SetCover(g, workID, "")
		})
		if err != nil {
			writeMutateError(w, err)
			return
		}
		// 204 promises the image is gone. A cover's bytes have their own
		// public, unauthenticated route, so a librarian acting on a rights
		// complaint must never be told a takedown happened when it did not.
		// Restore the statement rather than orphan bytes that keep serving:
		// nothing else indexes them, and there is no reconciliation pass
		// (tasks/266).
		if err := sweepStaleCovers(r, bs, workID, ""); err != nil {
			if rerr := restoreCover(r, bs, ix, workID, previous); rerr != nil {
				logger.Error("cover bytes survived a delete and the record could not be restored: the bytes are public and orphaned",
					"workId", workID, "cover", previous, "actor", id.Email, "delete", err, "restore", rerr)
				writeError(w, http.StatusInternalServerError,
					"the cover's bytes could not be removed and the record could not be restored: the cover is no longer listed but is still being served")
				return
			}
			logger.Error("cover bytes could not be removed and are still public",
				"workId", workID, "cover", previous, "actor", id.Email, "err", err)
			writeError(w, http.StatusInternalServerError,
				"the cover's bytes could not be removed and are still being served; the record is unchanged")
			return
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
		data, etag, err := bs.Get(r.Context(), bibframe.CoverBlobPath(workID, ext))
		if err != nil {
			writeError(w, http.StatusNotFound, "no such cover")
			return
		}
		// A same-format replacement keeps the URL, so without a validator every
		// cache between the store and the reader served the old image for up to
		// an hour after a correction. The server was right; the readers were not
		// (tasks/243).
		quoted := `"` + etag + `"`
		w.Header().Set("ETag", quoted)
		w.Header().Set("Cache-Control", "public, max-age=3600, must-revalidate")
		// The bytes are sniffed on upload, but a blob predating that check could
		// still be anything; nosniff retires the question.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if match := r.Header.Get("If-None-Match"); match != "" && (match == quoted || match == "*") {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(data)
	})
}
