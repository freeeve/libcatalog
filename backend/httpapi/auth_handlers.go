package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/auth/local"
)

// registerLocalAuth mounts the built-in user routes: login/refresh/logout for
// everyone, user administration for admins.
func registerLocalAuth(mux *http.ServeMux, svc *local.Service, verifier auth.TokenVerifier) {
	mux.HandleFunc("POST /v1/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Email, Password string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		tokens, err := svc.Login(r.Context(), req.Email, req.Password)
		switch {
		case errors.Is(err, local.ErrRateLimited):
			writeError(w, http.StatusTooManyRequests, "too many attempts")
		case err != nil:
			writeError(w, http.StatusUnauthorized, "bad credentials")
		default:
			writeJSON(w, http.StatusOK, tokens)
		}
	})
	mux.HandleFunc("POST /v1/auth/refresh", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ RefreshToken string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RefreshToken == "" {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		tokens, err := svc.Refresh(r.Context(), req.RefreshToken)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid refresh token")
			return
		}
		writeJSON(w, http.StatusOK, tokens)
	})
	mux.HandleFunc("POST /v1/auth/logout", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ RefreshToken string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		_ = svc.Logout(r.Context(), req.RefreshToken)
		w.WriteHeader(http.StatusNoContent)
	})
	if verifier == nil {
		return
	}
	adminOnly := auth.Require(verifier, auth.RoleAdmin)
	mux.Handle("GET /v1/users", adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		users, err := svc.ListUsers(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list failed")
			return
		}
		if users == nil {
			users = []local.UserInfo{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"users": users})
	})))
	mux.Handle("POST /v1/users", adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email    string      `json:"email"`
			Name     string      `json:"name"`
			Password string      `json:"password"`
			Roles    []auth.Role `json:"roles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		err := svc.CreateUser(r.Context(), req.Email, req.Name, req.Password, req.Roles)
		switch {
		case errors.Is(err, local.ErrUserExists):
			writeError(w, http.StatusConflict, "user exists")
		case err != nil:
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			w.WriteHeader(http.StatusCreated)
		}
	})))
	mux.Handle("PUT /v1/users/{email}/roles", adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Roles []auth.Role `json:"roles"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		err := svc.SetRoles(r.Context(), r.PathValue("email"), req.Roles)
		switch {
		case errors.Is(err, local.ErrUserNotFound):
			writeError(w, http.StatusNotFound, "no such user")
		case err != nil:
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})))
	mux.Handle("DELETE /v1/users/{email}", adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := svc.DeleteUser(r.Context(), r.PathValue("email"))
		switch {
		case errors.Is(err, local.ErrUserNotFound):
			writeError(w, http.StatusNotFound, "no such user")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "delete failed")
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})))
}
