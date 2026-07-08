package appdeps_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/aws/aws-lambda-go/events"

	"github.com/freeeve/libcat/backend/appdeps"
	"github.com/freeeve/libcat/backend/awslambda"
	"github.com/freeeve/libcat/backend/config"
	"github.com/freeeve/libcat/backend/httpapi"
)

// TestLambdaServesReadOnlyDemo builds the read-only demo deps the same way
// cmd/lcatd-lambda does and drives them through the Lambda adapter, proving the
// Function-URL/API-GW shape serves the SPA, /config, and the API -- and that the
// read-only guard rejects writes -- with only an in-memory store and a local
// (empty) grain dir, i.e. no AWS persistence.
func TestLambdaServesReadOnlyDemo(t *testing.T) {
	cfg := config.Config{
		ListenAddr:        ":0",
		LocalAuth:         true,
		LocalIssuer:       "lcatd-local",
		BootstrapAdmin:    "demo@example.org:demopass1",
		BlobDir:           t.TempDir(),
		AuthoritiesPrefix: "data/authorities/",
		Provider:          "marc",
		ReadOnly:          true,
	}
	deps, err := appdeps.Build(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("appdeps.Build: %v", err)
	}
	if !deps.ReadOnly {
		t.Fatal("expected deps.ReadOnly with a wrapped blob store")
	}
	handler := awslambda.Handler(httpapi.New(deps))

	invoke := func(method, path string) events.APIGatewayV2HTTPResponse {
		ev := events.APIGatewayV2HTTPRequest{RawPath: path}
		ev.RequestContext.HTTP.Method = method
		resp, err := handler(context.Background(), ev)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		return resp
	}

	if r := invoke("GET", "/v1/healthz"); r.StatusCode != 200 {
		t.Errorf("healthz status = %d, want 200", r.StatusCode)
	}
	if r := invoke("GET", "/config"); r.StatusCode != 200 || !strings.Contains(r.Body, `"readOnly":true`) {
		t.Errorf("/config = %d %s", r.StatusCode, r.Body)
	}
	// The SPA is served through Lambda (placeholder or real build, both are the
	// libcat app shell).
	if r := invoke("GET", "/"); r.StatusCode != 200 || !strings.Contains(r.Body, "libcat") {
		t.Errorf("GET / = %d (len %d)", r.StatusCode, len(r.Body))
	}
	// The read-only guard rejects an editorial write even over Lambda.
	if r := invoke("POST", "/v1/review"); r.StatusCode != 403 {
		t.Errorf("POST /v1/review = %d, want 403 (read-only guard)", r.StatusCode)
	}
}
