package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/vocabsrc"
)

// defaultUploadCapMB bounds a hand-uploaded dump unless the deployment says
// otherwise (LCATD_VOCAB_UPLOAD_CAP_MB). The install is synchronous and
// in-memory, so some ceiling must exist; gzipped N-Triples run ~10x smaller,
// and even the multi-GB national files fit compressed.
const defaultUploadCapMB = 512

// registerVocabSources mounts the authority-source surface (tasks/067): the
// click-to-download vocabulary list (librarian), registry edits and download/
// remove actions (admin), and the live typeahead proxy the picker uses
// (librarian -- the backend proxies so browser CORS and third-party endpoints
// stay out of the SPA).
func registerVocabSources(mux *http.ServeMux, svc *vocabsrc.Service, verifier auth.TokenVerifier, uploadCapMB int) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	admin := auth.Require(verifier, auth.RoleAdmin)
	if uploadCapMB <= 0 {
		uploadCapMB = defaultUploadCapMB
	}
	uploadCap := int64(uploadCapMB) << 20

	mux.Handle("GET /v1/vocabsources", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		views, err := svc.Views(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sources": views})
	})))

	mux.Handle("POST /v1/vocabsources", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var src vocabsrc.Source
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&src); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if err := svc.PutSource(r.Context(), src); err != nil {
			writeVocabSrcError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, src)
	})))

	mux.Handle("DELETE /v1/vocabsources/{name}", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := svc.DeleteSource(r.Context(), r.PathValue("name")); err != nil {
			writeVocabSrcError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
	})))

	mux.Handle("POST /v1/vocabsources/{name}/download", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		job, err := svc.CreateDownload(r.Context(), id.Email, r.PathValue("name"))
		if err != nil {
			writeVocabSrcError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	})))

	mux.Handle("DELETE /v1/vocabsources/{name}/snapshot", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := svc.RemoveSnapshot(r.Context(), r.PathValue("name")); err != nil {
			writeVocabSrcError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"removed": true})
	})))

	// Upload a dump by hand (tasks/067 follow-up): the body is the raw SKOS
	// N-Triples/N-Quads (optionally gzipped, sniffed) -- the escape hatch
	// when a publisher's download URL is unreachable. Installs synchronously.
	mux.Handle("PUT /v1/vocabsources/{name}/snapshot", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		terms, err := svc.InstallUpload(r.Context(), r.PathValue("name"), http.MaxBytesReader(w, r.Body, uploadCap))
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("the dump exceeds the %dMB upload cap -- gzip it (.nt.gz/.nq.gz install fine, ~10x smaller), or raise LCATD_VOCAB_UPLOAD_CAP_MB", uploadCapMB))
			return
		}
		if err != nil {
			writeVocabSrcError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"installed": true, "terms": terms})
	})))

	// Cache a live pick (tasks/072): the picked term's label and exactMatch
	// siblings land in the local index so the subject resolves forever.
	mux.Handle("POST /v1/vocabcache", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sugg vocabsrc.Suggestion
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&sugg); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if err := svc.CacheTerm(r.Context(), sugg); err != nil {
			writeVocabSrcError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"cached": true})
	})))

	mux.Handle("GET /v1/vocabsuggest", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("source")
		q := r.URL.Query().Get("q")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		src, err := svc.GetSource(r.Context(), name)
		if err != nil {
			writeVocabSrcError(w, err)
			return
		}
		client := svc.Suggest
		if client == nil {
			client = &vocabsrc.SuggestClient{}
		}
		suggestions, err := client.Suggest(r.Context(), src, q, limit)
		if err != nil {
			if errors.Is(err, vocabsrc.ErrValidation) {
				writeVocabSrcError(w, err)
				return
			}
			writeError(w, http.StatusBadGateway, "suggest source unavailable")
			return
		}
		if suggestions == nil {
			suggestions = []vocabsrc.Suggestion{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"suggestions": suggestions})
	})))
}

func writeVocabSrcError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, vocabsrc.ErrValidation):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, vocabsrc.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such source")
	case errors.Is(err, vocabsrc.ErrConflict):
		// The reason is the whole message here -- it names what to do first.
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "vocab source operation failed")
	}
}
