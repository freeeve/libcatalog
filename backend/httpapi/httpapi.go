// Package httpapi assembles the backend's HTTP surface as a plain
// net/http.Handler, independent of how it is served: cmd/lcatd wraps it in a
// listener, cmd/lcatd-lambda wraps it in the Lambda runtime. Handlers arrive
// in later tasks; this package owns routing, middleware, and response
// conventions.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/auth/local"
	"github.com/freeeve/libcat/backend/authoritiesvc"
	"github.com/freeeve/libcodex/sip2"

	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/copycat"
	"github.com/freeeve/libcat/backend/enrich"
	"github.com/freeeve/libcat/backend/export"
	"github.com/freeeve/libcat/backend/profilesvc"
	"github.com/freeeve/libcat/backend/publish"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/backend/vocabsrc"
	"github.com/freeeve/libcat/backend/workindex"
	"github.com/freeeve/libcat/storage/blob"
)

// Deps carries the services handlers depend on. It grows as tasks land;
// everything in it is an interface so tests inject fakes.
type Deps struct {
	// Logger receives request logs and handler errors. nil disables logging.
	Logger *slog.Logger
	// Blob is the grain store. Record and export handlers (later tasks)
	// read and publish through it.
	Blob blob.Store
	// WorkIndex is the shared identity/summary index over the work grains
	//. Optional: New builds one over Blob when nil. A deployment
	// passes its own to share the index with services that write grains
	// outside httpapi (copycat, workers) or to warm it at boot.
	WorkIndex *workindex.Index
	// Verifier authenticates staff bearer tokens (an auth.Multi when both
	// SSO and local users are configured). nil leaves staff routes
	// unregistered.
	Verifier auth.TokenVerifier
	// AuthExchange, when set, serves POST /v1/auth/exchange -- the OIDC
	// PKCE token-exchange proxy for SPA logins against an external issuer.
	AuthExchange http.Handler
	// Local, when set, mounts built-in user auth (login/refresh/logout)
	// and, with Verifier, admin user management.
	Local *local.Service
	// Vocab, when set, mounts GET /v1/terms autocomplete over the loaded
	// controlled vocabularies.
	Vocab *vocab.Index
	// Suggest and Abuse together mount the anonymous suggestion surface
	// (challenge, submit, public counts).
	Suggest *suggest.Service
	Abuse   *suggest.Abuse
	// Authorities, when set, mounts the local-authority editing surface
	// and hooks the on-save auto-linker into record writes.
	Authorities *authoritiesvc.Service
	// Batch, when set, mounts batch operations, macros, and saved queries.
	Batch *batch.Service
	// Profiles is the live editing-profile set the record/batch/authority
	// surfaces map through. New synthesizes a defaults-only, read-only
	// service when this is nil, so the field is optional for tests.
	Profiles *profilesvc.Service
	// Copycat, when set, mounts external search and staged imports.
	Copycat *copycat.Service
	// Publisher, when set, carries approved decisions into the grain store
	// (POST /v1/publish and the review publish flag).
	Publisher GraphPublisher
	// DB is the document store backing drafts (and, with Blob and Verifier,
	// enables the record-editing surface).
	DB store.Store
	// Exports, when set, mounts the export-job surface.
	Exports *export.Service
	// QueueMinConfidence is the review queue's default confidence floor:
	// PIPELINE suggestions below it are hidden unless a request sets its
	// own minConfidence (0 shows everything -- the default).
	QueueMinConfidence float64
	// SIP2, when set, mounts the public availability bridge (the OPAC's
	// proxied transport for live shelf status over SIP2).
	SIP2 *sip2.Client
	// OrgCode is the deployment's MARC organization code; MARC surfaces
	// derive each record's 040 from graph facts at decode time when set.
	OrgCode string
	// Enrich, when set, mounts the admin enrichment surface.
	Enrich *enrich.Service
	// VocabSources, when set, mounts the authority-source registry, the
	// vocabulary download list, and the live suggest proxy.
	VocabSources *vocabsrc.Service
	// VocabUploadCapMB bounds hand-uploaded vocabulary dumps (0 = the
	// 512MB default). The install is synchronous and in-memory, so a
	// deployment sizes this to its own RAM appetite.
	VocabUploadCapMB int
	// UI, when set, serves the embedded cataloging SPA at "/" (API routes
	// keep priority under /v1/).
	UI http.Handler
	// ClientConfig is the JSON the SPA boots from (GET /config): auth
	// modes, issuers, vocab schemes, provider -- deployment facts, never
	// secrets.
	ClientConfig map[string]any
	// ExtraFacets lists the extras keys the works view facets on
	//, e.g. "sources" for provenance triage.
	ExtraFacets []string
	// ReadOnly puts the instance in demo mode: editorial and config writes are
	// rejected (paired with a read-only blob store), while authentication,
	// reads, search, and dry-run previews still work.
	ReadOnly bool
	// Health, when set, backs GET /v1/readyz. A nil Health reports ready
	// forever, which is what a non-orchestrated deployment wants.
	Health *Health
}

// GraphPublisher is the publish pipeline seam (publish.Publisher in
// production; fakes in tests).
type GraphPublisher interface {
	PublishApproved(ctx context.Context, actor string) (publish.Result, error)
}

// New assembles the routed, middleware-wrapped API handler.
func New(deps Deps) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/healthz", handleHealthz)
	mux.HandleFunc("GET /v1/readyz", handleReadyz(deps.Health))
	// A defaults-only, read-only profile set stands in when no service is
	// wired (tests, or a deployment without a blob store), so the record,
	// batch, and authority handlers always have a profile source.
	if deps.Profiles == nil {
		deps.Profiles = profilesvc.New(nil, "", deps.Logger)
		_ = deps.Profiles.Load(context.Background())
	}
	// One shared work index for every consumer (records handlers, the
	// queue's title join): defaulting it here rather than inside the
	// records block means a caller who set only Blob still gets one.
	if deps.WorkIndex == nil && deps.Blob != nil {
		deps.WorkIndex = workindex.New(deps.Blob, "data/works/")
	}
	if deps.AuthExchange != nil {
		mux.Handle("POST /v1/auth/exchange", deps.AuthExchange)
	}
	if deps.Local != nil {
		registerLocalAuth(mux, deps.Local, deps.Verifier, deps.Suggest, deps.Batch)
	}
	if deps.Vocab != nil {
		registerTerms(mux, deps.Vocab, deps.Suggest)
	}
	if deps.Suggest != nil && deps.Abuse != nil {
		registerSuggestions(mux, deps.Suggest, deps.Abuse)
	}
	if deps.Suggest != nil && deps.Verifier != nil {
		registerReview(mux, deps.Suggest, deps.Verifier, deps.Publisher, deps.WorkIndex, deps.QueueMinConfidence)
	}
	// The auto-linker seam stays a nil interface unless a service is
	// configured (a typed-nil *Service must not masquerade as a hook).
	var hook WorkSaveHook
	if deps.Authorities != nil {
		hook = deps.Authorities
	}
	if deps.Blob != nil && deps.DB != nil && deps.Verifier != nil {
		ix := deps.WorkIndex
		registerRecords(mux, deps.Blob, ix, deps.DB, deps.Suggest, deps.Profiles, deps.Vocab, deps.Verifier, hook)
		registerMARC(mux, deps.Blob, ix, deps.Suggest, deps.Profiles, deps.Vocab, deps.Verifier, deps.OrgCode)
		registerMaintenance(mux, deps.Blob, ix, deps.Suggest, deps.Verifier)
		registerCovers(mux, deps.Blob, ix, deps.Suggest, deps.Verifier, deps.Logger)
		registerClone(mux, deps.Blob, ix, deps.Suggest, deps.Verifier)
		registerCoverBatch(mux, deps.Blob, ix, deps.Suggest, deps.Verifier, deps.Logger)
		registerRelations(mux, deps.Blob, ix, deps.Suggest, deps.Verifier, deps.Logger)
		registerAttachments(mux, deps.Blob, ix, deps.Suggest, deps.Verifier, deps.Logger)
		wl := registerWorksList(mux, ix, deps.Verifier, deps.ExtraFacets, deps.Vocab)
		registerTags(mux, wl, deps.Verifier)
		registerWorksSimilar(mux, ix, deps.Verifier, deps.Vocab)
		cws := &crosswalkSource{bs: deps.Blob}
		computeAudit := registerAudit(mux, ix, deps.Verifier, cws)
		registerAuditSnapshots(mux, deps.Blob, deps.Verifier, computeAudit)
		registerAuditCrosswalk(mux, deps.Blob, ix, deps.Verifier, cws)
		registerAuditTerms(mux, ix, deps.Vocab, deps.Verifier)
	}
	if deps.Authorities != nil && deps.Verifier != nil {
		registerAuthorities(mux, deps.Authorities, deps.Profiles, deps.Verifier, deps.Logger)
	}
	if deps.Verifier != nil {
		registerProfiles(mux, deps.Profiles, deps.Suggest, deps.Verifier)
	}
	if deps.Batch != nil && deps.Verifier != nil {
		registerBatch(mux, deps.Batch, deps.Verifier)
	}
	if deps.Copycat != nil && deps.Verifier != nil {
		registerCopycat(mux, deps.Copycat, deps.Verifier, deps.Suggest)
	}
	if deps.Copycat != nil && deps.Blob != nil && deps.Verifier != nil {
		registerSubjectLookup(mux, deps.Copycat, deps.Blob, deps.Vocab, deps.Verifier)
	}
	if deps.Suggest != nil && deps.Verifier != nil {
		registerPromotions(mux, deps.Suggest, deps.Publisher, deps.Verifier, deps.Logger)
		registerSuggestionPolicy(mux, deps.Suggest, deps.Verifier)
	}
	if deps.Exports != nil && deps.Verifier != nil {
		registerExports(mux, deps.Exports, deps.Batch, deps.Verifier)
	}
	if deps.Enrich != nil && deps.Verifier != nil {
		registerEnrich(mux, deps.Enrich, deps.Verifier, deps.Logger)
	}
	if deps.SIP2 != nil {
		registerAvailability(mux, deps.SIP2, deps.Logger)
	}
	if deps.VocabSources != nil && deps.Verifier != nil {
		registerVocabSources(mux, deps.VocabSources, deps.Verifier, deps.VocabUploadCapMB, deps.Suggest)
	}
	if deps.ClientConfig != nil {
		mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, deps.ClientConfig)
		})
	}
	if deps.UI != nil {
		// /v1/ is an API namespace: with the SPA catch-all mounted, an
		// unmatched path or method must answer as an API (JSON 404), never
		// fall through to index.html with 200 text/html -- which made route
		// typos and unsupported methods read as successes.
		// ServeMux prefers this over "/" and every registered /v1 route
		// over this; a catch-all also preempts the mux's native 405, so
		// wrong-method requests answer 404 here (route-table tracking
		// would buy a proper Allow header; not worth it yet). Without a
		// UI the mux's own 404/405 behavior stands.
		mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "no such endpoint")
		})
		mux.Handle("/", deps.UI)
	}
	var handler http.Handler = mux
	if deps.ReadOnly {
		handler = readOnlyGuard(handler)
	}
	return wrap(handler, deps.Logger)
}
