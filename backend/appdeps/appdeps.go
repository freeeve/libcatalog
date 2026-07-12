// Package appdeps assembles the backend's handler dependencies from resolved
// configuration, shared by both entrypoints: cmd/lcatd (the standalone/container
// server) and cmd/lcatd-lambda (the AWS Lambda function). Keeping the wiring in
// one place means the two shapes serve an identical surface.
//
// Background workers (the vocabulary-download and export-job drains) run on a
// ticker suited to a long-lived container; they are skipped in read-only mode
// (nothing queues writes for them to drain) so a read-only Lambda -- whose
// process freezes between invocations -- does not spawn them. A *writable*
// Lambda needs a different worker model (EventBridge/SQS or a scheduled drain);
// that is deferred (see the deployment task).
package appdeps

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"time"

	"github.com/freeeve/libcodex/sip2"

	"github.com/freeeve/libcat/ingest/bibliocommons"
	"github.com/freeeve/libcat/ingest/locsh"
	"github.com/freeeve/libcat/ingest/openlibrary"
	"github.com/freeeve/libcat/ingest/wikidata"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/auth/local"
	"github.com/freeeve/libcat/backend/auth/oidc"
	"github.com/freeeve/libcat/backend/authoritiesvc"
	"github.com/freeeve/libcat/backend/awsstore"
	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/config"
	"github.com/freeeve/libcat/backend/copycat"
	"github.com/freeeve/libcat/backend/enrich"
	"github.com/freeeve/libcat/backend/export"
	"github.com/freeeve/libcat/backend/httpapi"
	"github.com/freeeve/libcat/backend/profilesvc"
	"github.com/freeeve/libcat/backend/publish"
	"github.com/freeeve/libcat/backend/sruenrich"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/trigger"
	"github.com/freeeve/libcat/backend/trigger/awstrigger"
	"github.com/freeeve/libcat/backend/ui"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/backend/vocabsrc"
	"github.com/freeeve/libcat/backend/workindex"
)

// Build assembles the handler dependencies from configuration. The document
// store is DynamoDB when LCATD_DYNAMO_TABLE is set and the grain store is S3
// when LCATD_S3_BUCKET is set; otherwise both fall back to the in-memory /
// local-directory stores, so a laptop or the demo runs with no AWS at all.
func Build(ctx context.Context, cfg config.Config, logger *slog.Logger) (httpapi.Deps, error) {
	deps := httpapi.Deps{Logger: logger, OrgCode: cfg.OrgCode, QueueMinConfidence: cfg.QueueMinConfidence}
	// A configured scheme filter always admits the local scheme, or a fresh
	// deployment could never index its first minted authority.
	vocabSchemes := cfg.VocabSchemes
	if len(vocabSchemes) > 0 && !slices.Contains(vocabSchemes, authoritiesvc.LocalScheme) {
		vocabSchemes = append(vocabSchemes, authoritiesvc.LocalScheme)
	}
	var db store.Store = store.NewMem()
	switch {
	case cfg.DynamoTable != "":
		d, err := awsstore.Dynamo(ctx, cfg.DynamoTable, cfg.ResolvedDynamoEndpoint())
		if err != nil {
			return httpapi.Deps{}, err
		}
		db = d
		logger.Info("document store", "backend", "dynamodb", "table", cfg.DynamoTable)
	case cfg.StoreDir != "":
		d, err := store.NewDir(cfg.StoreDir)
		if err != nil {
			return httpapi.Deps{}, fmt.Errorf("open document store %s: %w", cfg.StoreDir, err)
		}
		db = d
		logger.Info("document store", "backend", "dir", "path", cfg.StoreDir)
	default:
		logger.Info("document store", "backend", "memory (resets on restart; set LCATD_STORE_DIR or LCATD_DYNAMO_TABLE to persist)")
	}
	deps.DB = db
	switch {
	case cfg.S3Bucket != "":
		b, err := awsstore.S3(ctx, cfg.S3Bucket, cfg.ResolvedS3Endpoint())
		if err != nil {
			return httpapi.Deps{}, err
		}
		deps.Blob = b
		logger.Info("blob store", "backend", "s3", "bucket", cfg.S3Bucket)
	case cfg.BlobDir != "":
		deps.Blob = blob.NewDir(cfg.BlobDir)
		logger.Info("blob store", "backend", "dir", "path", cfg.BlobDir)
	}
	// Read-only demo mode: wrap the grain store so no grain (or blob-backed
	// config -- profiles, vocab snapshots) ever persists, and let the HTTP guard
	// reject editorial/config writes. Wrapping here means every service built
	// below (which captures deps.Blob) inherits the read-only view. The document
	// store stays writable, so boot seeding, the bootstrap admin, and login
	// (refresh tokens) keep working; the HTTP guard blocks editorial store
	// writes at request time.
	if cfg.ReadOnly && deps.Blob != nil {
		deps.Blob = blob.ReadOnly(deps.Blob)
		deps.ReadOnly = true
		logger.Info("read-only mode", "enabled", true)
	}
	// The shared work index: built here rather than inside httpapi
	// so it can warm in the background while the server starts, instead of the
	// first editor request paying the corpus scan.
	if deps.Blob != nil {
		widx := workindex.New(deps.Blob, "data/works/")
		deps.WorkIndex = widx
		// Prime from the persisted snapshot before serving so a cold start skips
		// the corpus scan; a missing/corrupt snapshot just leaves the
		// index empty to warm from the store.
		if err := widx.LoadSnapshot(ctx); err != nil {
			logger.Warn("work index snapshot load", "err", err)
		}
		go func() {
			if err := widx.Refresh(ctx); err != nil {
				logger.Warn("work index warm-up", "err", err)
				return
			}
			// A snapshot whose ETags mostly missed bought nothing: it was
			// likely built against a different store backend.
			if primed, refetched := widx.SnapshotDrift(); primed > 0 && refetched*2 >= primed {
				logger.Warn("work index snapshot etag drift -- snapshot likely built against a different store backend; rebuild with lcatd workindex-snapshot against this store",
					"primed", primed, "refetched", refetched)
			}
			// Persist the reconciled projection so the next cold start is cheap.
			// Skipped read-only: the store rejects writes.
			if !cfg.ReadOnly {
				if err := widx.Save(ctx); err != nil {
					logger.Warn("work index snapshot save", "err", err)
				}
			}
		}()
	}
	if deps.Blob != nil {
		// The authority-source service resolves the effective scheme filter
		// (configured base + installed snapshots) before the index loads, so
		// installed vocabularies survive restarts.
		vsrc := &vocabsrc.Service{
			DB: db, Blob: deps.Blob, AuthoritiesPrefix: cfg.AuthoritiesPrefix,
			BaseSchemes: vocabSchemes, MaxSnapshotMB: cfg.VocabSnapshotCapMB,
			Logger: logger,
		}
		schemes, err := vsrc.Schemes(ctx)
		if err != nil {
			return httpapi.Deps{}, fmt.Errorf("resolve vocab schemes: %w", err)
		}
		// The index mounts even when empty: local-authority creation is
		// what populates it, and Reload swaps terms in as they land.
		ix, err := vocab.Load(ctx, deps.Blob, cfg.AuthoritiesPrefix, schemes)
		if err != nil {
			return httpapi.Deps{}, fmt.Errorf("load vocabularies: %w", err)
		}
		deps.Vocab = ix
		vsrc.Index = ix
		deps.VocabSources = vsrc
		deps.VocabUploadCapMB = cfg.VocabUploadCapMB
		if schemes := ix.Schemes(); len(schemes) > 0 {
			logger.Info("vocabularies loaded", "schemes", schemes)
		}
		// Container worker: drain queued vocabulary downloads on a ticker.
		// Serverless entrypoints disable tickers and drain on schedule.
		if !cfg.ReadOnly && !cfg.DisableTickers {
			go func() {
				ticker := time.NewTicker(15 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if _, err := vsrc.RunQueued(ctx); err != nil && ctx.Err() == nil {
							logger.Error("vocab download worker", "err", err)
						}
					}
				}
			}()
		}
	}
	if cfg.AbuseSecret != "" {
		abuse, err := suggest.NewAbuse([]byte(cfg.AbuseSecret))
		if err != nil {
			return httpapi.Deps{}, err
		}
		deps.Abuse = abuse
		deps.Suggest = suggest.New(db, deps.Vocab, suggest.Caps{})
		// Intake work gate: suggestions need a live work, so ghost rows
		// (ids the catalog never had, or retired) never enter the queue.
		if widx := deps.WorkIndex; widx != nil {
			deps.Suggest.WorkState = func(ctx context.Context, workID string) (bool, bool, error) {
				sums, paths, err := widx.SummariesWithPaths(ctx)
				if err != nil {
					return false, false, err
				}
				if _, ok := paths[workID]; !ok {
					return false, false, nil
				}
				for i := range sums {
					if sums[i].WorkID == workID {
						return true, sums[i].Tombstoned, nil
					}
				}
				return true, false, nil
			}
		}
	}
	var fan trigger.Fanout
	if cfg.WebhookURL != "" {
		fan = append(fan, trigger.Webhook{URL: cfg.WebhookURL, Secret: []byte(cfg.WebhookSecret)})
	}
	if cfg.RebuildCmd != "" {
		fan = append(fan, &trigger.Command{Cmd: cfg.RebuildCmd, Dir: cfg.RebuildDir, Logger: logger})
	}
	// Async job dispatch: a queue worker runs the incremental
	// rebuild instead of a synchronous local command.
	if cfg.TriggerSQSURL != "" {
		q, err := awstrigger.NewSQS(ctx, cfg.TriggerSQSURL, cfg.AWSEndpoint)
		if err != nil {
			return httpapi.Deps{}, err
		}
		fan = append(fan, q)
		logger.Info("trigger", "transport", "sqs", "queue", cfg.TriggerSQSURL)
	}
	if cfg.TriggerEventBus != "" {
		eb, err := awstrigger.NewEventBridge(ctx, cfg.TriggerEventBus, "lcatd", cfg.AWSEndpoint)
		if err != nil {
			return httpapi.Deps{}, err
		}
		fan = append(fan, eb)
		logger.Info("trigger", "transport", "eventbridge", "bus", cfg.TriggerEventBus)
	}
	var notifier trigger.Notifier = trigger.Noop{}
	if len(fan) > 0 {
		notifier = fan
	}
	// A publish burst coalesces into one trigger event.
	if cfg.RebuildDebounce > 0 && len(fan) > 0 {
		notifier = &trigger.Coalesce{Next: notifier, Window: cfg.RebuildDebounce, Logger: logger}
		logger.Info("trigger", "debounce", cfg.RebuildDebounce)
	}
	if deps.Suggest != nil && deps.Blob != nil {
		pub := &publish.Publisher{
			Blob: deps.Blob, Queue: deps.Suggest, Vocab: deps.Vocab,
			Trigger: notifier, Lease: publish.NewLease(db, "ingest", 15*time.Minute),
			Summaries: deps.WorkIndex, Logger: logger,
		}
		// Keep the shared index exact for publish writes, like the
		// single-record and batch paths. Guarded: a
		// typed-nil *workindex.Index must not masquerade as an updater.
		if deps.WorkIndex != nil {
			pub.Index = deps.WorkIndex
		}
		deps.Publisher = pub
	}
	if deps.Blob != nil {
		// The live editing-profile set: shipped defaults overlaid with the
		// deployment's blob-persisted overrides, editable at runtime.
		profSvc := profilesvc.New(deps.Blob, "", logger)
		if err := profSvc.Load(ctx); err != nil {
			return httpapi.Deps{}, fmt.Errorf("load profiles: %w", err)
		}
		deps.Profiles = profSvc
		deps.Authorities = &authoritiesvc.Service{
			Blob: deps.Blob, Vocab: deps.Vocab, Queue: deps.Suggest,
			Trigger: notifier, AuthoritiesPrefix: cfg.AuthoritiesPrefix,
			Schemes: vocabSchemes, Summaries: deps.WorkIndex, Logger: logger,
		}
		if deps.VocabSources != nil {
			deps.Authorities.SchemesFn = deps.VocabSources.Schemes
		}
		deps.Batch = &batch.Service{
			Blob: deps.Blob, DB: db, MapperFn: profSvc.Mapper,
			Queue: deps.Suggest, Trigger: notifier, Summaries: deps.WorkIndex,
			Labels: deps.Vocab.LabelResolver(), Logger: logger,
		}
		// Keep the shared index exact for batch writes, like the
		// single-record path. Guarded: a typed-nil
		// *workindex.Index must not masquerade as an updater.
		if deps.WorkIndex != nil {
			deps.Batch.Index = deps.WorkIndex
		}
		deps.Copycat = &copycat.Service{
			Blob: deps.Blob, DB: db, Queue: deps.Suggest, Trigger: notifier,
			Index: deps.WorkIndex,
		}
		if err := deps.Copycat.SeedDefaultTargets(ctx); err != nil {
			logger.Warn("seed default copycat targets", "err", err)
		}
	}
	verifiers := map[string]auth.TokenVerifier{}
	if cfg.LocalAuth {
		key, err := signingKey(cfg.LocalSigningKey, logger)
		if err != nil {
			return httpapi.Deps{}, err
		}
		svc, err := local.New(db, key, cfg.LocalIssuer)
		if err != nil {
			return httpapi.Deps{}, err
		}
		if restored, err := svc.Bootstrap(ctx, cfg.BootstrapAdmin); restored {
			logger.Warn("bootstrap re-granted admin to an existing demoted user", "spec", "LCATD_BOOTSTRAP_ADMIN")
		} else if err != nil {
			return httpapi.Deps{}, fmt.Errorf("bootstrap admin: %w", err)
		}
		deps.Local = svc
		verifiers[cfg.LocalIssuer] = svc
	}
	if cfg.OIDCIssuer != "" {
		roleMap := map[string]auth.Role{}
		for from, to := range cfg.OIDCRoleMap {
			roleMap[from] = auth.Role(to)
		}
		verifier, err := oidc.New(ctx, oidc.Config{
			Issuer:    cfg.OIDCIssuer,
			Audience:  cfg.OIDCAudience,
			RoleClaim: cfg.OIDCRoleClaim,
			RoleMap:   roleMap,
		})
		if err != nil {
			return httpapi.Deps{}, err
		}
		verifiers[cfg.OIDCIssuer] = verifier
		deps.AuthExchange = oidc.ExchangeHandler(oidc.ExchangeConfig{
			Issuer:       cfg.OIDCIssuer,
			ClientID:     cfg.OIDCClientID,
			ClientSecret: cfg.OIDCClientSecret,
		})
	}
	if len(verifiers) > 0 {
		deps.Verifier = auth.NewMulti(verifiers)
	}
	var brandCSS []byte
	if cfg.BrandCSS != "" {
		css, err := os.ReadFile(cfg.BrandCSS)
		if err != nil {
			return httpapi.Deps{}, fmt.Errorf("read LCATD_BRAND_CSS: %w", err)
		}
		brandCSS = css
	}
	deps.UI = ui.Handler(brandCSS)
	if ui.IsPlaceholder() {
		logger.Warn("cataloging SPA not built into this binary; the browser UI shows a build notice (run 'npm run build' in backend/ui before 'go build'). The JSON API is unaffected.")
	}
	deps.ExtraFacets = cfg.ExtraFacets
	clientCfg := map[string]any{
		"apiBase":   "", // same-origin
		"localAuth": cfg.LocalAuth,
		"provider":  cfg.Provider,
		"readOnly":  cfg.ReadOnly,
		"sandbox":   cfg.Sandbox,
	}
	if len(cfg.ExtraFacets) > 0 {
		clientCfg["extraFacets"] = cfg.ExtraFacets
	}
	if cfg.OIDCIssuer != "" {
		clientCfg["oidc"] = map[string]string{"issuer": cfg.OIDCIssuer, "clientId": cfg.OIDCClientID}
	}
	if deps.Vocab != nil {
		schemes := deps.Vocab.Schemes()
		if deps.Suggest != nil {
			schemes = append(schemes, vocab.FolkScheme)
		}
		clientCfg["schemes"] = schemes
	}
	deps.ClientConfig = clientCfg
	enrichSources := map[string]enrich.Source{}
	if cfg.EnrichLocsh != "" {
		enrichSources[locsh.Name] = enrich.Source{
			Enricher: locsh.New(), Mode: enrich.Mode(cfg.EnrichLocsh), Scheme: "lcsh",
		}
	}
	// OpenLibrary external-identity enrichment: build the ISBN -> work
	// index from the configured offline dump once at boot, then link Works by exact
	// ISBN. Direct mode writes the owl:sameAs; queue-mode moderation of identity
	// links is a later concern (the queue path handles subject candidates, not
	// sameAs), so identities only surface under direct today.
	if cfg.EnrichOpenLibrary != "" {
		f, err := os.Open(cfg.EnrichOpenLibraryDump)
		if err != nil {
			return httpapi.Deps{}, fmt.Errorf("open OpenLibrary dump: %w", err)
		}
		index, err := openlibrary.ReadEditionsDump(f)
		f.Close()
		if err != nil {
			return httpapi.Deps{}, fmt.Errorf("read OpenLibrary dump: %w", err)
		}
		logger.Info("openlibrary enrichment index built", "isbns", len(index), "mode", cfg.EnrichOpenLibrary)
		enrichSources[openlibrary.Name] = enrich.Source{
			Enricher: openlibrary.New(index), Mode: enrich.Mode(cfg.EnrichOpenLibrary), Scheme: openlibrary.Scheme,
		}
	}
	// Wikidata creator demographics: opt-in, direct-only. Resolution is by
	// cataloged ISBN against the public Query Service (rate-limited inside
	// the enricher); claims land in enrichment:wikidata with provenance.
	if cfg.EnrichWikidata != "" {
		opts := []wikidata.Option{wikidata.WithLogger(logger)}
		if cfg.EnrichWikidataEndpoint != "" {
			opts = append(opts, wikidata.WithEndpoint(cfg.EnrichWikidataEndpoint))
			logger.Info("wikidata enrichment endpoint overridden", "endpoint", cfg.EnrichWikidataEndpoint)
		}
		enrichSources[wikidata.Name] = enrich.Source{
			Enricher: wikidata.New(opts...), Mode: enrich.Mode(cfg.EnrichWikidata),
		}
	}
	// Every registered suggest-capable authority source doubles as a
	// moderated enrichment target -- admin-triggered, queue mode.
	if deps.VocabSources != nil && deps.Suggest != nil {
		sources, err := deps.VocabSources.Sources(ctx)
		if err != nil {
			return httpapi.Deps{}, fmt.Errorf("list vocab sources: %w", err)
		}
		for _, src := range sources {
			if !src.CanSuggest() {
				continue
			}
			if _, taken := enrichSources[src.Name]; taken {
				continue
			}
			// The local vocab index (when the scheme is installed) upgrades
			// matches to full term descriptions and rides their ancestor
			// chains along.
			enr := vocabsrc.NewEnricher(src, nil)
			enr.Index = deps.Vocab
			enrichSources[src.Name] = enrich.Source{
				Enricher: enr, Mode: enrich.ModeQueue, Scheme: src.Scheme,
			}
		}
	}
	// Every loaded vocabulary is a crosswalk target: walking
	// subjects' exactMatch/closeMatch links into it queues moderated
	// equivalent-heading suggestions. Vocabularies installed later join at
	// the next restart.
	if deps.Vocab != nil && deps.Suggest != nil {
		for _, scheme := range deps.Vocab.Schemes() {
			xw := vocabsrc.NewCrosswalk(deps.Vocab, scheme)
			if _, taken := enrichSources[xw.Name()]; taken {
				continue
			}
			enrichSources[xw.Name()] = enrich.Source{Enricher: xw, Mode: enrich.ModeQueue, Scheme: scheme}
		}
	}
	// The SRU/Z39.50 subject harvester: asks the copycat targets what a
	// scoped set of works is about and queues ONLY reconciled controlled
	// terms for moderation -- external subject cataloging without
	// whole-record import. Scheme stays empty: harvested terms span
	// vocabularies, so each derives its scheme from its URI.
	if deps.Copycat != nil && deps.Vocab != nil && deps.Suggest != nil {
		enrichSources[sruenrich.Name] = enrich.Source{
			Enricher: &sruenrich.Enricher{Search: deps.Copycat, Vocab: deps.Vocab, Delay: time.Second, Log: logger},
			Mode:     enrich.ModeQueue,
		}
	}
	// The BiblioCommons peer-library subject harvest: drives subject searches
	// on a peer OPAC's public RSS from the configured vocabulary's terms and
	// queues the driver term on ISBN/title+author-matched works. Queue-only
	// -- a peer's cataloging is a candidate, not an assertion.
	if cfg.EnrichBiblioCommons != "" && deps.Vocab != nil && deps.Suggest != nil {
		scheme := cfg.EnrichBiblioCommonsScheme
		var terms []bibliocommons.Term
		for _, t := range deps.Vocab.Terms(scheme) {
			if t.MergedInto != "" {
				continue
			}
			if q := t.Label("en"); q != "" {
				terms = append(terms, bibliocommons.Term{URI: t.ID, Labels: t.Labels, Query: q})
			}
		}
		if len(terms) == 0 {
			logger.Warn("bibliocommons enrichment disabled: driver vocabulary has no terms loaded", "scheme", scheme)
		} else {
			opts := []bibliocommons.Option{bibliocommons.WithLogger(logger)}
			if cfg.EnrichBiblioCommonsMaxPages > 0 {
				opts = append(opts, bibliocommons.WithMaxPages(cfg.EnrichBiblioCommonsMaxPages))
			}
			enrichSources[bibliocommons.Name] = enrich.Source{
				Enricher: bibliocommons.New(cfg.EnrichBiblioCommons, terms, opts...),
				Mode:     enrich.ModeQueue, Scheme: scheme,
			}
			logger.Info("bibliocommons subject harvest configured",
				"host", cfg.EnrichBiblioCommons, "scheme", scheme, "terms", len(terms))
		}
	}
	if len(enrichSources) > 0 && deps.Blob != nil {
		deps.Enrich = &enrich.Service{Blob: deps.Blob, DB: db, Queue: deps.Suggest, Sources: enrichSources, Summaries: deps.WorkIndex}
		// Container worker: drain queued enrichment jobs on a ticker, like
		// exports. Serverless entrypoints disable tickers and drain on
		// schedule.
		if !cfg.ReadOnly && !cfg.DisableTickers {
			enrichSvc := deps.Enrich
			go func() {
				ticker := time.NewTicker(15 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if _, err := enrichSvc.RunQueuedJobs(ctx); err != nil && ctx.Err() == nil {
							logger.Error("enrichment job worker", "err", err)
						}
					}
				}
			}()
		}
	}
	// The SIP2 availability bridge: mounted whenever an ACS address is
	// configured; credentials stay server-side, the OPAC posts item ids.
	if cfg.SIP2Addr != "" {
		deps.SIP2 = &sip2.Client{
			Address: cfg.SIP2Addr, User: cfg.SIP2User, Password: cfg.SIP2Pass,
			Location: cfg.SIP2Location, InstitutionID: cfg.SIP2Institution,
			ErrorDetection: cfg.SIP2ErrorDetection,
		}
		logger.Info("sip2 availability bridge", "addr", cfg.SIP2Addr, "login", cfg.SIP2User != "")
	}
	if deps.Blob != nil && cfg.AbuseSecret != "" {
		exports, err := export.New(db, deps.Blob, cfg.Provider, []byte(cfg.AbuseSecret))
		if err != nil {
			return httpapi.Deps{}, err
		}
		exports.Vocab = deps.Vocab
		exports.OrgCode = cfg.OrgCode
		deps.Exports = exports
		// Container worker: drain queued export jobs on a ticker.
		// Serverless entrypoints disable tickers and drain on schedule.
		if !cfg.ReadOnly && !cfg.DisableTickers {
			go func() {
				ticker := time.NewTicker(15 * time.Second)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if _, err := exports.RunQueued(ctx); err != nil && ctx.Err() == nil {
							logger.Error("export worker", "err", err)
						}
					}
				}
			}()
		}
	}
	return deps, nil
}

// signingKey decodes the configured Ed25519 key (seed or full private key,
// base64 std or raw-url), or generates an ephemeral one for dev. Under
// Lambda an ephemeral key is an error, not a warning: each concurrent
// sandbox would mint its own key, so tokens issued by one sandbox fail
// verification on the next -- intermittent 401s, not just
// sessions-die-on-restart. The same applies to any
// horizontally-scaled deployment, which we cannot detect; the warning says
// so.
func signingKey(encoded string, logger *slog.Logger) (ed25519.PrivateKey, error) {
	if encoded == "" {
		if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
			return nil, fmt.Errorf("LCATD_LOCAL_SIGNING_KEY must be set under Lambda: concurrent sandboxes each minting an ephemeral key make token verification fail across instances")
		}
		logger.Warn("LCATD_LOCAL_SIGNING_KEY unset; generating ephemeral key (sessions die on restart, and break across instances if this deployment scales past one)")
		_, key, err := ed25519.GenerateKey(rand.Reader)
		return key, err
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(encoded)
	}
	if err != nil {
		return nil, fmt.Errorf("decode signing key: %w", err)
	}
	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(raw), nil
	}
	return nil, fmt.Errorf("signing key must be a %d-byte seed or %d-byte private key", ed25519.SeedSize, ed25519.PrivateKeySize)
}
