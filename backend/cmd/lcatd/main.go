// Command lcatd serves the libcatalog dynamic backend as a standalone HTTP
// server -- the container / self-host deployment shape. The same handler runs
// under AWS Lambda via cmd/lcatd-lambda.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/freeeve/libcatalog/backend/config"
	"github.com/freeeve/libcatalog/backend/httpapi"
	"github.com/freeeve/libcatalog/storage/blob"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := config.FromEnv()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}
	deps := httpapi.Deps{Logger: logger}
	if cfg.BlobDir != "" {
		deps.Blob = blob.NewDir(cfg.BlobDir)
	}
	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           httpapi.New(deps),
		ReadHeaderTimeout: 10 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
