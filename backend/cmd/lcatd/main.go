// Command lcatd serves the libcat dynamic backend as a standalone HTTP
// server -- the container / self-host deployment shape. The same handler runs
// under AWS Lambda via cmd/lcatd-lambda.
package main

import (
	"context"
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

	"github.com/freeeve/libcat/backend/appdeps"
	"github.com/freeeve/libcat/backend/config"
	"github.com/freeeve/libcat/backend/httpapi"
	"github.com/freeeve/libcat/backend/profiles"
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
	// "lcatd workindex-snapshot (--blob-dir <dir> | --s3-bucket <bucket>)" --
	// build the work-index snapshot offline so a fresh deployment's first cold
	// start skips the corpus scan (tasks/155). Must run against the store the
	// snapshot will serve; ETag schemes differ per backend (tasks/162).
	if len(os.Args) > 1 && os.Args[1] == "workindex-snapshot" {
		if err := runWorkindexSnapshot(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	// "lcatd vocab-install --name <source> --url <dump>" -- install a vocabulary
	// snapshot into a blob store offline, for deployments whose async download
	// worker never runs (Lambda, tasks/163).
	if len(os.Args) > 1 && os.Args[1] == "vocab-install" {
		if err := runVocabInstall(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	cfg, err := config.FromEnv()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	deps, err := appdeps.Build(ctx, cfg, logger)
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
