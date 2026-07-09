package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/auth/local"
	"github.com/freeeve/libcat/backend/suggest"
)

// registerLocalAuth mounts the built-in user routes: login/refresh/logout for
// everyone, user administration for admins. queue, when set, receives one
// audit entry per user mutation -- role grants are the privilege boundary
// for every other audited action, so they must be legible too (tasks/208).
func registerLocalAuth(mux *http.ServeMux, svc *local.Service, verifier auth.TokenVerifier, queue *suggest.Service) {
	// audit names the acting admin and the affected account.
	audit := func(r *http.Request, action, note string, terms []string) {
		if queue == nil {
			return
		}
		actor := ""
		if id, ok := auth.FromContext(r.Context()); ok {
			actor = id.Email
		}
		queue.WriteAudit(r.Context(), suggest.AuditEntry{Action: action, Actor: actor, Note: note, Terms: terms})
	}
	// selfTarget reports whether the acting admin is the {email} target --
	// self-service on the admin surface is refused so a slip stays
	// recoverable by a second admin (tasks/207).
	selfTarget := func(r *http.Request) bool {
		id, ok := auth.FromContext(r.Context())
		return ok && strings.EqualFold(strings.TrimSpace(id.Email), strings.TrimSpace(r.PathValue("email")))
	}
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
			audit(r, "USER_CREATE", req.Email, roleTerms(req.Roles))
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
		if selfTarget(r) {
			writeError(w, http.StatusForbidden, "admins cannot change their own role")
			return
		}
		// Old roles ride the audit note so a demotion is legible without
		// diffing against a prior entry (tasks/208).
		var oldRoles []auth.Role
		if before, err := svc.GetUser(r.Context(), r.PathValue("email")); err == nil {
			oldRoles = before.Roles
		}
		err := svc.SetRoles(r.Context(), r.PathValue("email"), req.Roles)
		switch {
		case errors.Is(err, local.ErrUserNotFound):
			writeError(w, http.StatusNotFound, "no such user")
		case errors.Is(err, local.ErrLastAdmin):
			writeError(w, http.StatusConflict, "cannot remove the last admin")
		case err != nil:
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			audit(r, "USER_ROLES", fmt.Sprintf("%s: %s -> %s", r.PathValue("email"), joinRoles(oldRoles), joinRoles(req.Roles)), roleTerms(req.Roles))
			w.WriteHeader(http.StatusNoContent)
		}
	})))
	mux.Handle("DELETE /v1/users/{email}", adminOnly(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if selfTarget(r) {
			writeError(w, http.StatusForbidden, "admins cannot delete their own account")
			return
		}
		err := svc.DeleteUser(r.Context(), r.PathValue("email"))
		switch {
		case errors.Is(err, local.ErrUserNotFound):
			writeError(w, http.StatusNotFound, "no such user")
		case errors.Is(err, local.ErrLastAdmin):
			writeError(w, http.StatusConflict, "cannot remove the last admin")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "delete failed")
		default:
			audit(r, "USER_DELETE", r.PathValue("email"), nil)
			w.WriteHeader(http.StatusNoContent)
		}
	})))
}

// roleTerms renders roles as audit term strings ("role:admin").
func roleTerms(roles []auth.Role) []string {
	out := make([]string, 0, len(roles))
	for _, r := range roles {
		out = append(out, "role:"+string(r))
	}
	return out
}

// joinRoles renders a role list for the audit note; "none" for empty.
func joinRoles(roles []auth.Role) string {
	if len(roles) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(roles))
	for _, r := range roles {
		parts = append(parts, string(r))
	}
	return strings.Join(parts, ",")
}
