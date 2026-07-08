package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/profiles"
	"github.com/freeeve/libcat/backend/profilesvc"
	"github.com/freeeve/libcat/backend/suggest"
)

// profileSummary is the list shape: the profile plus whether a deployment
// override currently shadows the shipped default.
type profileSummary struct {
	*profiles.Profile
	Overridden bool `json:"overridden"`
}

// registerProfiles mounts the editing-profile surface: the op-builder's read
// list (librarian), and the admin runtime editor (get one, save an override,
// revert to default). Saves are validated by profilesvc before they persist,
// so a bad profile is rejected here rather than breaking a cataloger's editor.
func registerProfiles(mux *http.ServeMux, prof *profilesvc.Service, queue *suggest.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	admin := auth.Require(verifier, auth.RoleAdmin)

	mux.Handle("GET /v1/profiles", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entries := prof.List()
		out := make(map[string]profileSummary, len(entries))
		for _, e := range entries {
			out[e.Profile.ID] = profileSummary{Profile: e.Profile, Overridden: e.Overridden}
		}
		writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
	})))

	mux.Handle("GET /v1/profiles/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, etag, overridden, err := prof.Get(r.PathValue("id"))
		if errors.Is(err, profilesvc.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such profile")
			return
		}
		if etag != "" {
			w.Header().Set("ETag", etag)
		}
		writeJSON(w, http.StatusOK, map[string]any{"profile": p, "etag": etag, "isDefault": !overridden})
	})))

	mux.Handle("PUT /v1/profiles/{id}", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		body, err := readProfileBody(w, r)
		if err != nil {
			return
		}
		etag, err := prof.Put(r.Context(), id, body, r.Header.Get("If-Match"))
		switch {
		case errors.Is(err, profilesvc.ErrReadOnly):
			writeError(w, http.StatusConflict, "profiles are read-only on this deployment")
		case errors.Is(err, profilesvc.ErrInvalid):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, profilesvc.ErrIDMismatch):
			writeError(w, http.StatusBadRequest, "profile id must match the URL")
		case errors.Is(err, profilesvc.ErrConflict):
			writeError(w, http.StatusPreconditionFailed, "profile changed since you loaded it")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "profile save failed")
		default:
			if queue != nil {
				a, _ := auth.FromContext(r.Context())
				queue.WriteAudit(r.Context(), suggest.AuditEntry{Action: "PROFILE_EDIT", Actor: a.Email, Note: id})
			}
			w.Header().Set("ETag", etag)
			writeJSON(w, http.StatusOK, map[string]any{"id": id, "etag": etag})
		}
	})))

	mux.Handle("DELETE /v1/profiles/{id}", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		err := prof.DeleteOverride(r.Context(), id)
		switch {
		case errors.Is(err, profilesvc.ErrReadOnly):
			writeError(w, http.StatusConflict, "profiles are read-only on this deployment")
		case errors.Is(err, profilesvc.ErrNotFound):
			writeError(w, http.StatusNotFound, "no override to revert")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "profile revert failed")
		default:
			if queue != nil {
				a, _ := auth.FromContext(r.Context())
				queue.WriteAudit(r.Context(), suggest.AuditEntry{Action: "PROFILE_REVERT", Actor: a.Email, Note: id})
			}
			w.WriteHeader(http.StatusNoContent)
		}
	})))
}

// readProfileBody reads a bounded profile document, writing a 400 on failure.
func readProfileBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad request body")
		return nil, err
	}
	if !json.Valid(body) {
		writeError(w, http.StatusBadRequest, "body is not valid JSON")
		return nil, errors.New("invalid json")
	}
	return body, nil
}
