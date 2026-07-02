// Command lcatd serves the libcatalog dynamic backend as a standalone HTTP
// server -- the container / self-host deployment shape. The same handler runs
// under AWS Lambda via cmd/lcatd-lambda.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/auth/local"
	"github.com/freeeve/libcatalog/backend/auth/oidc"
	"github.com/freeeve/libcatalog/ingest/locsh"

	"github.com/freeeve/libcatalog/backend/config"
	"github.com/freeeve/libcatalog/backend/enrich"
	"github.com/freeeve/libcatalog/backend/export"
	"github.com/freeeve/libcatalog/backend/httpapi"
	"github.com/freeeve/libcatalog/backend/profiles"
	"github.com/freeeve/libcatalog/backend/publish"
	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/suggest"
	"github.com/freeeve/libcatalog/backend/trigger"
	"github.com/freeeve/libcatalog/backend/ui"
	"github.com/freeeve/libcatalog/backend/vocab"
	"github.com/freeeve/libcatalog/storage/blob"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	// "lcatd profiles-validate [dir]" -- the framework-test subcommand: load
	// the shipped editing profiles plus a deployment's overrides and exit
	// nonzero on any invalid profile.
	if len(os.Args) > 1 && os.Args[1] == "profiles-validate" {
		set, err := profiles.LoadDefaults()
		if err == nil && len(os.Args) > 2 {
			set, err = profiles.LoadDir(set, os.Args[2])
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		ids := make([]string, 0, len(set))
		for id := range set {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		fmt.Printf("%d profiles valid: %s\n", len(ids), strings.Join(ids, ", "))
		return
	}
	cfg, err := config.FromEnv()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	deps, err := buildDeps(ctx, cfg, logger)
	if err != nil {
		logger.Error("setup", "err", err)
		os.Exit(1)
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpapi.New(deps),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("shutdown", "err", err)
		}
	}()
	logger.Info("listening", "addr", cfg.ListenAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("serve", "err", err)
		os.Exit(1)
	}
}

// buildDeps assembles the handler dependencies from configuration. The
// datastore is in-memory for now; the DynamoDB selection arrives with the
// deployment task.
func buildDeps(ctx context.Context, cfg config.Config, logger *slog.Logger) (httpapi.Deps, error) {
	deps := httpapi.Deps{Logger: logger}
	if cfg.BlobDir != "" {
		deps.Blob = blob.NewDir(cfg.BlobDir)
		ix, err := vocab.Load(ctx, deps.Blob, cfg.AuthoritiesPrefix, cfg.VocabSchemes)
		if err != nil {
			return httpapi.Deps{}, fmt.Errorf("load vocabularies: %w", err)
		}
		if schemes := ix.Schemes(); len(schemes) > 0 {
			logger.Info("vocabularies loaded", "schemes", schemes)
			deps.Vocab = ix
		}
	}
	db := store.NewMem()
	deps.DB = db
	if cfg.AbuseSecret != "" {
		abuse, err := suggest.NewAbuse([]byte(cfg.AbuseSecret))
		if err != nil {
			return httpapi.Deps{}, err
		}
		deps.Abuse = abuse
		deps.Suggest = suggest.New(db, deps.Vocab, suggest.Caps{})
	}
	if deps.Suggest != nil && deps.Blob != nil {
		var notifier trigger.Notifier = trigger.Noop{}
		if cfg.WebhookURL != "" {
			notifier = trigger.Webhook{URL: cfg.WebhookURL, Secret: []byte(cfg.WebhookSecret)}
		}
		deps.Publisher = &publish.Publisher{
			Blob: deps.Blob, Queue: deps.Suggest, Vocab: deps.Vocab,
			Trigger: notifier, Lease: publish.NewLease(db, "ingest", 15*time.Minute),
			Logger: logger,
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
	clientCfg := map[string]any{
		"apiBase":   "", // same-origin
		"localAuth": cfg.LocalAuth,
		"provider":  cfg.Provider,
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
	if cfg.EnrichLocsh != "" && deps.Blob != nil {
		deps.Enrich = &enrich.Service{
			Blob: deps.Blob, Queue: deps.Suggest,
			Sources: map[string]enrich.Source{
				locsh.Name: {Enricher: locsh.New(), Mode: enrich.Mode(cfg.EnrichLocsh), Scheme: "lcsh"},
			},
		}
	}
	if deps.Blob != nil && cfg.AbuseSecret != "" {
		exports, err := export.New(db, deps.Blob, cfg.Provider, []byte(cfg.AbuseSecret))
		if err != nil {
			return httpapi.Deps{}, err
		}
		deps.Exports = exports
		// Container worker: drain queued export jobs on a ticker.
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
