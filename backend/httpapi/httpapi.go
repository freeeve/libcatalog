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

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/auth/local"
	"github.com/freeeve/libcatalog/backend/authoritiesvc"
	"github.com/freeeve/libcatalog/backend/batch"
	"github.com/freeeve/libcatalog/backend/enrich"
	"github.com/freeeve/libcatalog/backend/export"
	"github.com/freeeve/libcatalog/backend/publish"
	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/suggest"
	"github.com/freeeve/libcatalog/backend/vocab"
	"github.com/freeeve/libcatalog/storage/blob"
)

// Deps carries the services handlers depend on. It grows as tasks land;
// everything in it is an interface so tests inject fakes.
type Deps struct {
	// Logger receives request logs and handler errors. nil disables logging.
	Logger *slog.Logger
	// Blob is the grain store. Record and export handlers (later tasks)
	// read and publish through it.
	Blob blob.Store
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
	// (tasks/046) and hooks the on-save auto-linker into record writes.
	Authorities *authoritiesvc.Service
	// Batch, when set, mounts batch operations, macros, and saved queries
	// (tasks/047).
	Batch *batch.Service
	// Publisher, when set, carries approved decisions into the grain store
	// (POST /v1/publish and the review publish flag).
	Publisher GraphPublisher
	// DB is the document store backing drafts (and, with Blob and Verifier,
	// enables the record-editing surface).
	DB store.Store
	// Exports, when set, mounts the export-job surface.
	Exports *export.Service
	// Enrich, when set, mounts the admin enrichment surface.
	Enrich *enrich.Service
	// UI, when set, serves the embedded cataloging SPA at "/" (API routes
	// keep priority under /v1/).
	UI http.Handler
	// ClientConfig is the JSON the SPA boots from (GET /config): auth
	// modes, issuers, vocab schemes, provider -- deployment facts, never
	// secrets.
	ClientConfig map[string]any
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
	if deps.AuthExchange != nil {
		mux.Handle("POST /v1/auth/exchange", deps.AuthExchange)
	}
	if deps.Local != nil {
		registerLocalAuth(mux, deps.Local, deps.Verifier)
	}
	if deps.Vocab != nil {
		registerTerms(mux, deps.Vocab, deps.Suggest)
	}
	if deps.Suggest != nil && deps.Abuse != nil {
		registerSuggestions(mux, deps.Suggest, deps.Abuse)
	}
	if deps.Suggest != nil && deps.Verifier != nil {
		registerReview(mux, deps.Suggest, deps.Verifier, deps.Publisher)
	}
	// The auto-linker seam stays a nil interface unless a service is
	// configured (a typed-nil *Service must not masquerade as a hook).
	var hook WorkSaveHook
	if deps.Authorities != nil {
		hook = deps.Authorities
	}
	if deps.Blob != nil && deps.DB != nil && deps.Verifier != nil {
		registerRecords(mux, deps.Blob, deps.DB, deps.Suggest, deps.Verifier, hook)
		registerMARC(mux, deps.Blob, deps.Suggest, deps.Verifier)
		wl := registerWorksList(mux, deps.Blob, deps.Verifier)
		registerTags(mux, wl, deps.Verifier)
	}
	if deps.Authorities != nil && deps.Verifier != nil {
		registerAuthorities(mux, deps.Authorities, deps.Verifier)
	}
	if deps.Batch != nil && deps.Verifier != nil {
		registerBatch(mux, deps.Batch, deps.Verifier)
	}
	if deps.Suggest != nil && deps.Verifier != nil {
		registerPromotions(mux, deps.Suggest, deps.Publisher, deps.Verifier)
	}
	if deps.Exports != nil && deps.Verifier != nil {
		registerExports(mux, deps.Exports, deps.Batch, deps.Verifier)
	}
	if deps.Enrich != nil && deps.Verifier != nil {
		registerEnrich(mux, deps.Enrich, deps.Verifier)
	}
	if deps.ClientConfig != nil {
		mux.HandleFunc("GET /config", func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, deps.ClientConfig)
		})
	}
	if deps.UI != nil {
		mux.Handle("/", deps.UI)
	}
	return wrap(mux, deps.Logger)
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
