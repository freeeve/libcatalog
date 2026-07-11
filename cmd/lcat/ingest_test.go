package main

import (
	"context"
	"strings"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

// emptyProvider scans zero records, like a mistyped cache path would if the
// provider did not guard it.
type emptyProvider struct{}

func (emptyProvider) Name() string                                         { return "empty" }
func (emptyProvider) Role() ingest.Role                                    { return ingest.RoleIngest }
func (emptyProvider) Records(ctx context.Context) ([]ingest.Record, error) { return nil, nil }

// TestRunIngestRefusesEmptyReconcile covers a zero-record scan must
// not reconcile (it would withdraw every feed-only work) unless explicitly
// allowed.
func TestRunIngestRefusesEmptyReconcile(t *testing.T) {
	reg := ingest.NewRegistry()
	if err := reg.Register("empty", func(ingest.Config) (ingest.Provider, error) {
		return emptyProvider{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()

	err := runIngest(reg, "empty", ingest.Config{}, out, "auto-suppress", false)
	if err == nil || !strings.Contains(err.Error(), "zero-record scan") {
		t.Fatalf("zero-record reconcile should refuse, got %v", err)
	}
	// The override reconciles the genuinely empty feed without error.
	if err := runIngest(reg, "empty", ingest.Config{}, out, "auto-suppress", true); err != nil {
		t.Fatalf("--reconcile-allow-empty run failed: %v", err)
	}
}
