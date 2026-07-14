package httpapi

import (
	"net/http"
	"strconv"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/enrich"
	"github.com/freeeve/libcat/backend/suggest"
)

// registerQueueActions wires filter-scoped bulk actions over the review queue.
// They run as async jobs on the enrichment board (which the /v1/review 100-cap
// makes necessary at queue scale) and are librarian-gated -- a bulk approval is
// heavier than a single moderator decision.
func registerQueueActions(mux *http.ServeMux, svc *suggest.Service, jobs *enrich.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	// POST /v1/queue/approve-all approves every PENDING suggestion matching the
	// queue filter, as a job. It is two-step by design: without ?confirm it
	// answers the count it WOULD act on; with ?confirm=<count> it starts the
	// job only when that count still matches, so an accept-all against the
	// wrong filter is caught before it runs. Approve never publishes, so the
	// run stays fully reversible (each approved-unpublished row is rejectable)
	// until a separate publish.
	mux.Handle("POST /v1/queue/approve-all", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		q, ok := parseApproveScope(w, r)
		if !ok {
			return
		}
		count, err := svc.CountPending(r.Context(), q)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "queue read failed")
			return
		}
		confirmRaw := r.URL.Query().Get("confirm")
		if confirmRaw == "" {
			// Dry run: hand back the count for the caller to echo.
			writeJSON(w, http.StatusOK, map[string]any{"count": count, "confirmRequired": true})
			return
		}
		confirm, err := strconv.Atoi(confirmRaw)
		if err != nil || confirm < 0 {
			writeError(w, http.StatusBadRequest, "confirm wants the count of rows to approve")
			return
		}
		if confirm != count {
			// The scope shifted under the caller (a concurrent harvest, another
			// reviewer): make them re-confirm against the current count.
			writeJSON(w, http.StatusConflict, map[string]any{
				"count": count, "confirm": confirm,
				"message": "the pending count changed; re-confirm with the current count",
			})
			return
		}
		if count == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"count": 0, "message": "nothing pending matches the filter"})
			return
		}
		job, err := jobs.CreateApproveAllJob(r.Context(), id.Email, q)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "could not start approve-all")
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	})))
}

// parseApproveScope reads the queue filter for a bulk action off the request,
// mirroring GET /v1/queue's params (scheme/provenance/type/minConfidence) with
// PENDING forced -- an approve-all acts only on pending rows. No deployment
// confidence floor is implied: a bulk action approves exactly what its filter
// names, and the two-step confirm makes that scope concrete first.
func parseApproveScope(w http.ResponseWriter, r *http.Request) (suggest.QueueQuery, bool) {
	q := suggest.QueueQuery{
		Status:     suggest.StatusPending,
		Scheme:     r.URL.Query().Get("scheme"),
		Provenance: suggest.Provenance(r.URL.Query().Get("provenance")),
		Type:       suggest.SuggType(r.URL.Query().Get("type")),
	}
	if raw := r.URL.Query().Get("minConfidence"); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil || v < 0 || v > 1 {
			writeError(w, http.StatusBadRequest, "minConfidence wants a number in [0,1]")
			return suggest.QueueQuery{}, false
		}
		q.MinConfidence = v
	}
	return q, true
}
