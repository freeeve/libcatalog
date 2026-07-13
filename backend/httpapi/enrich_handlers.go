package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/freeeve/libcat/ingest"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/enrich"
)

// registerEnrich mounts the admin enrichment surface: list configured
// sources and kick a run. Runs execute synchronously in-request (sources are
// batched and bounded); a scheduled/worker execution path is a deployment
// concern layered on the same service.
func registerEnrich(mux *http.ServeMux, svc *enrich.Service, verifier auth.TokenVerifier, logger *slog.Logger) {
	admin := auth.Require(verifier, auth.RoleAdmin)
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	mux.Handle("GET /v1/enrich", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"sources": svc.Names()})
	})))

	// ?filter=key=value (repeatable, ANDed; comma-joined extras match per
	// element) and ?source= scope the run to matching works -- the same
	// predicate the diversity audit uses. An external-service source then
	// queries for exactly the scoped set, which is both quota hygiene and
	// data minimization: sensitive enrichment (creator demographics) can be
	// generated for a curated sub-collection only.
	mux.Handle("POST /v1/enrich/{source}/run", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filters, err := auditFilters(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var keep func(*ingest.WorkSummary) bool
		if len(filters) > 0 {
			keep = func(s *ingest.WorkSummary) bool { return filters.match(s.Extras) }
		}
		source := r.PathValue("source")
		result, err := svc.Run(r.Context(), source, keep)
		if err != nil {
			writeEnrichRunError(w, logger, source, err)
			return
		}
		result.Scope = filters.String()
		writeJSON(w, http.StatusOK, result)
	})))

	// The async path for corpus-scale runs (a synchronous wikidata pass can
	// hold the request open for tens of minutes): kick returns 202 + a job
	// id immediately, a worker drains the queue, and GET polls the record --
	// with the enricher's live batch counters while it runs. Same ?filter/
	// ?source scoping as the synchronous run.
	mux.Handle("POST /v1/enrich/{source}/jobs", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filters, err := auditFilters(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		id, _ := auth.FromContext(r.Context())
		// ?hosts=seattle,sfpl -- the per-job peer override for sources
		// that take one; validated in CreateJob.
		var hosts []string
		if raw := strings.TrimSpace(r.URL.Query().Get("hosts")); raw != "" {
			for _, h := range strings.Split(raw, ",") {
				if h = strings.TrimSpace(h); h != "" {
					hosts = append(hosts, h)
				}
			}
		}
		job, err := svc.CreateJob(r.Context(), id.Email, r.PathValue("source"), filters, hosts)
		if err != nil {
			writeEnrichRunError(w, logger, r.PathValue("source"), err)
			return
		}
		writeJSON(w, http.StatusAccepted, job)
	})))

	mux.Handle("GET /v1/enrich/jobs", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jobs, err := svc.ListJobs(r.Context())
		if err != nil {
			logger.Error("enrichment job list failed", "err", err)
			writeError(w, http.StatusInternalServerError, "job list failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
	})))

	mux.Handle("GET /v1/enrich/jobs/{id}", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		job, err := svc.GetJob(r.Context(), r.PathValue("id"))
		if errors.Is(err, enrich.ErrJobNotFound) {
			writeError(w, http.StatusNotFound, "unknown enrichment job")
			return
		}
		if err != nil {
			logger.Error("enrichment job read failed", "id", r.PathValue("id"), "err", err)
			writeError(w, http.StatusInternalServerError, "job read failed")
			return
		}
		writeJSON(w, http.StatusOK, job)
	})))
}

// writeEnrichRunError maps a run failure to the status its cause deserves,
// so automation retries transient upstream faults (5xx) and gives up on its
// own mistakes (4xx). The raw error -- which can carry upstream URLs and
// storage paths -- goes to the server log; the client gets a generic message.
func writeEnrichRunError(w http.ResponseWriter, logger *slog.Logger, source string, err error) {
	switch {
	case errors.Is(err, enrich.ErrUnknownSource):
		writeError(w, http.StatusNotFound, "unknown enrichment source")
	case errors.Is(err, enrich.ErrValidation):
		// The caller's mistake (bad host, hosts on a source that takes
		// none) -- say what, not just that.
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, context.DeadlineExceeded):
		logger.Error("enrichment run timed out", "source", source, "err", err)
		writeError(w, http.StatusGatewayTimeout, "enrichment upstream timed out")
	case errors.Is(err, ingest.ErrEnricher):
		logger.Error("enrichment upstream failed", "source", source, "err", err)
		writeError(w, http.StatusBadGateway, "enrichment upstream failed")
	default:
		// Storage faults and source misconfiguration alike: the
		// deployment's problem, never the caller's.
		logger.Error("enrichment run failed", "source", source, "err", err)
		writeError(w, http.StatusInternalServerError, "enrichment run failed")
	}
}
