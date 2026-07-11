// Command lcatd-lambda serves the libcat dynamic backend under AWS Lambda
// behind a Function URL (or API Gateway v2 HTTP API; both deliver the v2 HTTP
// payload). provided.al2023, handler "bootstrap". It builds the same handler as
// cmd/lcatd from the same LCATD_* environment, so a read-only demo runs on
// Lambda with the SPA embedded and no persistent AWS services (in-memory store
// + a bundled read-only grain dir).
//
// A writable deployment (DynamoDB + S3 via LCATD_DYNAMO_TABLE /
// LCATD_S3_BUCKET) additionally needs its queued work drained: container
// tickers freeze between invocations, so the function also accepts the
// scheduled drain event {"lcatd":"drain"} (an EventBridge rule in the
// terraform stack) and runs the vocab-download and export-job drains once
// per firing instead.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"

	"github.com/aws/aws-lambda-go/events"
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
	// The init context is the process lifetime. Ticker workers are replaced
	// by the scheduled drain event on Lambda -- a frozen process never
	// advances a goroutine.
	cfg.DisableTickers = true
	deps, err := appdeps.Build(context.Background(), cfg, logger)
	if err != nil {
		logger.Error("setup", "err", err)
		os.Exit(1)
	}
	api := awslambda.Handler(httpapi.New(deps))
	lambda.Start(func(ctx context.Context, raw json.RawMessage) (any, error) {
		if isDrainEvent(raw) {
			return drain(ctx, deps, logger)
		}
		var ev events.APIGatewayV2HTTPRequest
		if err := json.Unmarshal(raw, &ev); err != nil {
			return nil, fmt.Errorf("lcatd-lambda: unrecognized event: %w", err)
		}
		return api(ctx, ev)
	})
}

// isDrainEvent matches the EventBridge schedule's constant input.
func isDrainEvent(raw json.RawMessage) bool {
	var probe struct {
		Lcatd string `json:"lcatd"`
	}
	return json.Unmarshal(raw, &probe) == nil && probe.Lcatd == "drain"
}

// drain runs each queued-work drain once, inside a live invocation, and
// reports what it moved. Errors log per drain and surface after both ran --
// one stuck queue must not starve the other.
func drain(ctx context.Context, deps httpapi.Deps, logger *slog.Logger) (map[string]int, error) {
	out := map[string]int{}
	var firstErr error
	if deps.VocabSources != nil {
		n, err := deps.VocabSources.RunQueued(ctx)
		out["vocabJobs"] = n
		if err != nil {
			logger.Error("drain vocab downloads", "err", err)
			firstErr = err
		}
	}
	if deps.Exports != nil {
		n, err := deps.Exports.RunQueued(ctx)
		out["exportJobs"] = n
		if err != nil {
			logger.Error("drain export jobs", "err", err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	logger.Info("drain complete", "vocabJobs", out["vocabJobs"], "exportJobs", out["exportJobs"])
	return out, firstErr
}
