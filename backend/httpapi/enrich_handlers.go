package httpapi

import (
	"net/http"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/enrich"
)

// registerEnrich mounts the admin enrichment surface: list configured
// sources and kick a run. Runs execute synchronously in-request (sources are
// batched and bounded); a scheduled/worker execution path is a deployment
// concern layered on the same service.
func registerEnrich(mux *http.ServeMux, svc *enrich.Service, verifier auth.TokenVerifier) {
	admin := auth.Require(verifier, auth.RoleAdmin)

	mux.Handle("GET /v1/enrich", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"sources": svc.Names()})
	})))

	mux.Handle("POST /v1/enrich/{source}/run", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := svc.Run(r.Context(), r.PathValue("source"))
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))
}
