// Command lcatd-lambda serves the libcat dynamic backend under AWS Lambda
// behind a Function URL (or API Gateway v2 HTTP API; both deliver the v2 HTTP
// payload). provided.al2023, handler "bootstrap". It builds the same handler as
// cmd/lcatd from the same LCATD_* environment, so a read-only demo runs on
// Lambda with the SPA embedded and no persistent AWS services (in-memory store
// + a bundled read-only grain dir).
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/lambda"

	"github.com/freeeve/libcat/backend/appdeps"
	"github.com/freeeve/libcat/backend/awslambda"
	"github.com/freeeve/libcat/backend/config"
	"github.com/freeeve/libcat/backend/httpapi"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	cfg, err := config.FromEnv()
	if err != nil {
		logger.Error("config", "err", err)
		os.Exit(1)
	}
	// The init context is the process lifetime; background workers are skipped in
	// read-only mode (appdeps), which is the supported Lambda shape today.
	deps, err := appdeps.Build(context.Background(), cfg, logger)
	if err != nil {
		logger.Error("setup", "err", err)
		os.Exit(1)
	}
	lambda.Start(awslambda.Handler(httpapi.New(deps)))
}
