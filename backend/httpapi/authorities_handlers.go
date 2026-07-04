package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/authoritiesvc"
	"github.com/freeeve/libcatalog/backend/profilesvc"
	"github.com/freeeve/libcatalog/backend/vocab"
)

// authorityView is the read shape of a local authority term.
type authorityView struct {
	ID   string                 `json:"id"`
	ETag string                 `json:"etag"`
	Term bibframe.AuthorityTerm `json:"term"`
}

// registerAuthorities mounts the librarian authorities surface (tasks/046):
// local-term CRUD with ETag optimistic locking, the profile the editor form
// renders from, merge, and the explicit index reload.
func registerAuthorities(mux *http.ServeMux, svc *authoritiesvc.Service, prof *profilesvc.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	// The editing form's field definitions -- the same live profile set the
	// record editor maps through, so a runtime edit to authority-topic shows
	// here without a restart.
	mux.Handle("GET /v1/authorities/profile", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, prof.Set()["authority-topic"])
	})))

	// Listing and label search over the local scheme (imported schemes are
	// served by /v1/terms; this surface manages what the deployment owns).
	mux.Handle("GET /v1/authorities", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		limit := termsDefaultLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		var terms []*vocab.Term
		if q == "" {
			terms = svc.Vocab.Terms(authoritiesvc.LocalScheme)
			if len(terms) > limit {
				terms = terms[:limit]
			}
		} else {
			terms = svc.Vocab.Search(authoritiesvc.LocalScheme, q, limit)
		}
		if terms == nil {
			terms = []*vocab.Term{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"terms": terms})
	})))

	mux.Handle("POST /v1/authorities", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var term bibframe.AuthorityTerm
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&term); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		term.MergedInto = "" // retirement is merge's to write, never a client field
		authID, etag, err := svc.Create(r.Context(), term, id.Email)
		if errors.Is(err, authoritiesvc.ErrValidation) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authority create failed")
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusCreated, map[string]string{
			"id": authID, "uri": bibframe.LocalAuthorityIRI(authID), "etag": etag,
		})
	})))

	mux.Handle("GET /v1/authorities/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authID := r.PathValue("id")
		if !authoritiesvc.IDPattern.MatchString(authID) {
			writeError(w, http.StatusBadRequest, "bad authority id")
			return
		}
		term, etag, err := svc.Get(r.Context(), authID)
		if errors.Is(err, blob.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such authority")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authority read failed")
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, authorityView{ID: authID, ETag: etag, Term: term})
	})))

	mux.Handle("PUT /v1/authorities/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		authID := r.PathValue("id")
		if !authoritiesvc.IDPattern.MatchString(authID) {
			writeError(w, http.StatusBadRequest, "bad authority id")
			return
		}
		ifMatch := r.Header.Get("If-Match")
		if ifMatch == "" {
			writeError(w, http.StatusPreconditionRequired, "If-Match required")
			return
		}
		var term bibframe.AuthorityTerm
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&term); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		etag, err := svc.Update(r.Context(), authID, term, ifMatch, id.Email)
		switch {
		case errors.Is(err, authoritiesvc.ErrValidation):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, blob.ErrNotFound):
			writeError(w, http.StatusNotFound, "no such authority")
			return
		case errors.Is(err, blob.ErrPreconditionFailed):
			// A concurrent write landed: return the fresh state so the
			// client rebases deliberately, mirroring the records surface.
			fresh, freshTag, err := svc.Get(r.Context(), authID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "authority read failed")
				return
			}
			w.Header().Set("ETag", freshTag)
			writeJSON(w, http.StatusPreconditionFailed, authorityView{ID: authID, ETag: freshTag, Term: fresh})
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "authority write failed")
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]string{"id": authID, "etag": etag})
	})))

	mux.Handle("POST /v1/authorities/merge", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Loser  string        `json:"loser"`
			Winner vocab.TermRef `json:"winner"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			!authoritiesvc.IDPattern.MatchString(req.Loser) || req.Winner.ID == "" || req.Winner.Scheme == "" {
			writeError(w, http.StatusBadRequest, "merge needs a local loser id and a winner term")
			return
		}
		result, err := svc.Merge(r.Context(), req.Loser, req.Winner, id.Email)
		switch {
		case errors.Is(err, authoritiesvc.ErrValidation):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, blob.ErrNotFound):
			writeError(w, http.StatusNotFound, "no such authority")
			return
		case err != nil:
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))

	mux.Handle("POST /v1/authorities/reload", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := svc.Reload(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "vocab reload failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"schemes": svc.Vocab.Schemes()})
	})))
}
