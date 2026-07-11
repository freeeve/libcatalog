package config

import "testing"

// TestStoreSelectionDefaults documents that a bare environment (the laptop/demo
// case) leaves the persistent-store knobs empty, so the caller falls back to
// the in-memory and local-directory stores.
func TestStoreSelectionDefaults(t *testing.T) {
	t.Setenv("LCATD_ABUSE_SECRET", "")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DynamoTable != "" || cfg.S3Bucket != "" || cfg.AWSEndpoint != "" {
		t.Fatalf("expected empty store knobs by default, got %+v", cfg)
	}
}

// TestEnrichOpenLibraryConfig locks the OpenLibrary enrichment knobs:
// a mode must be queue/direct, and enabling it requires the dump path.
func TestEnrichOpenLibraryConfig(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		t.Setenv("LCATD_ENRICH_OPENLIBRARY", "direct")
		t.Setenv("LCATD_ENRICH_OPENLIBRARY_DUMP", "/data/ol_editions.txt")
		cfg, err := FromEnv()
		if err != nil {
			t.Fatal(err)
		}
		if cfg.EnrichOpenLibrary != "direct" || cfg.EnrichOpenLibraryDump != "/data/ol_editions.txt" {
			t.Errorf("cfg = %+v", cfg)
		}
	})
	t.Run("bad mode rejected", func(t *testing.T) {
		t.Setenv("LCATD_ENRICH_OPENLIBRARY", "auto")
		t.Setenv("LCATD_ENRICH_OPENLIBRARY_DUMP", "/data/ol.txt")
		if _, err := FromEnv(); err == nil {
			t.Error("expected an error for a mode that is not queue/direct")
		}
	})
	t.Run("enabled without a dump rejected", func(t *testing.T) {
		t.Setenv("LCATD_ENRICH_OPENLIBRARY", "direct")
		t.Setenv("LCATD_ENRICH_OPENLIBRARY_DUMP", "")
		if _, err := FromEnv(); err == nil {
			t.Error("expected an error: the source needs a dump path")
		}
	})
}

// TestStoreSelectionFromEnv locks the env-var names that opt into the
// persistent stores.
func TestStoreSelectionFromEnv(t *testing.T) {
	t.Setenv("LCATD_DYNAMO_TABLE", "lcat-sidecar")
	t.Setenv("LCATD_S3_BUCKET", "lcat-grains")
	t.Setenv("LCATD_AWS_ENDPOINT", "http://localhost:4566")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DynamoTable != "lcat-sidecar" {
		t.Errorf("DynamoTable = %q", cfg.DynamoTable)
	}
	if cfg.S3Bucket != "lcat-grains" {
		t.Errorf("S3Bucket = %q", cfg.S3Bucket)
	}
	if cfg.AWSEndpoint != "http://localhost:4566" {
		t.Errorf("AWSEndpoint = %q", cfg.AWSEndpoint)
	}
}
