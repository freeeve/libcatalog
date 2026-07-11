package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
)

// registerSuggestionPolicy mounts the patron-suggestion policy surface
// (tasks/263): the admin reads and edits the stored, opt-in policy, and every
// write is audited (tasks/259 -- no unaudited config surface). A public read
// exposes just the policy so the discovery site can hide the "suggest a
// subject" affordance rather than offering a control that 403s. Enforcement is
// in suggest.Service (resolveTerm), not here, so it governs any caller of
// Submit, not just this route.
func registerSuggestionPolicy(mux *http.ServeMux, svc *suggest.Service, verifier auth.TokenVerifier) {
	admin := auth.Require(verifier, auth.RoleAdmin)

	audit := func(r *http.Request, action, note string) {
		actor := ""
		if id, ok := auth.FromContext(r.Context()); ok {
			actor = id.Email
		}
		svc.WriteAudit(r.Context(), suggest.AuditEntry{Action: action, Actor: actor, Note: note})
	}

	mux.Handle("GET /v1/config/suggestions", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pol, err := svc.GetPolicy(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "policy read failed")
			return
		}
		writeJSON(w, http.StatusOK, pol)
	})))

	mux.Handle("PUT /v1/config/suggestions", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var pol suggest.Policy
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&pol); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		saved, err := svc.PutPolicy(r.Context(), pol)
		if errors.Is(err, suggest.ErrBadPolicy) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "policy write failed")
			return
		}
		// Record the resulting policy, so the trail answers "who opened patron
		// suggestions, to which schemes, and what free-text mode".
		state := "disabled"
		if saved.Enabled {
			state = "enabled"
		}
		schemes := "all loaded schemes"
		if len(saved.Schemes) > 0 {
			schemes = fmt.Sprintf("%v", saved.Schemes)
		}
		audit(r, "SUGGESTION_POLICY_SET", fmt.Sprintf("%s; schemes %s; free-text %s", state, schemes, saved.FreeText))
		writeJSON(w, http.StatusOK, saved)
	})))

	// Public: the discovery site reads this to decide whether to show the
	// suggestion affordance. It carries no secret -- the allowlist and mode are
	// exactly what a patron's submission is checked against.
	mux.Handle("GET /v1/suggestions/policy", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pol, err := svc.GetPolicy(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "policy read failed")
			return
		}
		writeJSON(w, http.StatusOK, pol)
	}))
}
