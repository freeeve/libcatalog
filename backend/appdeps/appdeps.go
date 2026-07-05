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
	"slices"
	"time"

	"github.com/freeeve/libcatalog/ingest/locsh"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/auth/local"
	"github.com/freeeve/libcatalog/backend/auth/oidc"
	"github.com/freeeve/libcatalog/backend/authoritiesvc"
	"github.com/freeeve/libcatalog/backend/awsstore"
	"github.com/freeeve/libcatalog/backend/batch"
	"github.com/freeeve/libcatalog/backend/config"
	"github.com/freeeve/libcatalog/backend/copycat"
	"github.com/freeeve/libcatalog/backend/enrich"
	"github.com/freeeve/libcatalog/backend/export"
	"github.com/freeeve/libcatalog/backend/httpapi"
	"github.com/freeeve/libcatalog/backend/profilesvc"
	"github.com/freeeve/libcatalog/backend/publish"
	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/suggest"
	"github.com/freeeve/libcatalog/backend/trigger"
	"github.com/freeeve/libcatalog/backend/ui"
	"github.com/freeeve/libcatalog/backend/vocab"
	"github.com/freeeve/libcatalog/backend/vocabsrc"
)

// Build assembles the handler dependencies from configuration. The document
// store is DynamoDB when LCATD_DYNAMO_TABLE is set and the grain store is S3
// when LCATD_S3_BUCKET is set; otherwise both fall back to the in-memory /
// local-directory stores, so a laptop or the demo runs with no AWS at all.
func Build(ctx context.Context, cfg config.Config, logger *slog.Logger) (httpapi.Deps, error) {
	deps := httpapi.Deps{Logger: logger}
	// A configured scheme filter always admits the local scheme, or a fresh
	// deployment could never index its first minted authority (tasks/046).
	vocabSchemes := cfg.VocabSchemes
	if len(vocabSchemes) > 0 && !slices.Contains(vocabSchemes, authoritiesvc.LocalScheme) {
		vocabSchemes = append(vocabSchemes, authoritiesvc.LocalScheme)
	}
	var db store.Store = store.NewMem()
	if cfg.DynamoTable != "" {
		d, err := awsstore.Dynamo(ctx, cfg.DynamoTable, cfg.AWSEndpoint)
		if err != nil {
			return httpapi.Deps{}, err
		}
		db = d
		logger.Info("document store", "backend", "dynamodb", "table", cfg.DynamoTable)
	} else {
		logger.Info("document store", "backend", "memory (resets on restart)")
	}
	deps.DB = db
	switch {
	case cfg.S3Bucket != "":
		b, err := awsstore.S3(ctx, cfg.S3Bucket, cfg.AWSEndpoint)
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
	if deps.Blob != nil {
		// The authority-source service resolves the effective scheme filter
		// (configured base + installed snapshots) before the index loads, so
		// installed vocabularies survive restarts (tasks/067).
		vsrc := &vocabsrc.Service{
			DB: db, Blob: deps.Blob, AuthoritiesPrefix: cfg.AuthoritiesPrefix,
			BaseSchemes: vocabSchemes, Logger: logger,
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
		if !cfg.ReadOnly {
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
	}
	var fan trigger.Fanout
	if cfg.WebhookURL != "" {
		fan = append(fan, trigger.Webhook{URL: cfg.WebhookURL, Secret: []byte(cfg.WebhookSecret)})
	}
	if cfg.RebuildCmd != "" {
		fan = append(fan, &trigger.Command{Cmd: cfg.RebuildCmd, Dir: cfg.RebuildDir, Logger: logger})
	}
	var notifier trigger.Notifier = trigger.Noop{}
	if len(fan) > 0 {
		notifier = fan
	}
	if deps.Suggest != nil && deps.Blob != nil {
		deps.Publisher = &publish.Publisher{
			Blob: deps.Blob, Queue: deps.Suggest, Vocab: deps.Vocab,
			Trigger: notifier, Lease: publish.NewLease(db, "ingest", 15*time.Minute),
			Logger: logger,
		}
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
			Schemes: vocabSchemes, Logger: logger,
		}
		if deps.VocabSources != nil {
			deps.Authorities.SchemesFn = deps.VocabSources.Schemes
		}
		deps.Batch = &batch.Service{
			Blob: deps.Blob, DB: db, MapperFn: profSvc.Mapper,
			Queue: deps.Suggest, Trigger: notifier,
		}
		deps.Copycat = &copycat.Service{
			Blob: deps.Blob, DB: db, Queue: deps.Suggest, Trigger: notifier,
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
		if err := svc.Bootstrap(ctx, cfg.BootstrapAdmin); err != nil {
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
	deps.UI = ui.Handler()
	if ui.IsPlaceholder() {
		logger.Warn("cataloging SPA not built into this binary; the browser UI shows a build notice (run 'npm run build' in backend/ui before 'go build'). The JSON API is unaffected.")
	}
	clientCfg := map[string]any{
		"apiBase":   "", // same-origin
		"localAuth": cfg.LocalAuth,
		"provider":  cfg.Provider,
		"readOnly":  cfg.ReadOnly,
		"sandbox":   cfg.Sandbox,
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
	// Every registered suggest-capable authority source doubles as a
	// moderated enrichment target (tasks/067) -- admin-triggered, queue mode.
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
			enrichSources[src.Name] = enrich.Source{
				Enricher: vocabsrc.NewEnricher(src, nil), Mode: enrich.ModeQueue, Scheme: src.Scheme,
			}
		}
	}
	// Every loaded vocabulary is a crosswalk target (tasks/072): walking
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
	if len(enrichSources) > 0 && deps.Blob != nil {
		deps.Enrich = &enrich.Service{Blob: deps.Blob, Queue: deps.Suggest, Sources: enrichSources}
	}
	if deps.Blob != nil && cfg.AbuseSecret != "" {
		exports, err := export.New(db, deps.Blob, cfg.Provider, []byte(cfg.AbuseSecret))
		if err != nil {
			return httpapi.Deps{}, err
		}
		exports.Vocab = deps.Vocab
		deps.Exports = exports
		// Container worker: drain queued export jobs on a ticker.
		if !cfg.ReadOnly {
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
// base64 std or raw-url), or generates an ephemeral one for dev.
func signingKey(encoded string, logger *slog.Logger) (ed25519.PrivateKey, error) {
	if encoded == "" {
		logger.Warn("LCATD_LOCAL_SIGNING_KEY unset; generating ephemeral key (sessions die on restart)")
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
