// Package config carries the backend's deployment configuration, read from
// the environment. It is deliberately SDK-free: cloud specifics (bucket names,
// table names, secret lookups) resolve to plain values before they reach the
// core, so the same configuration surface serves a container, a Lambda, and a
// laptop.
package config

import (
	"fmt"
	"os"
)

// Config is the resolved backend configuration.
type Config struct {
	// ListenAddr is the HTTP listen address for the standalone server
	// (ignored under Lambda). Default ":8080".
	ListenAddr string
	// BlobDir, when set, selects a local-directory grain store rooted there.
	// Cloud blob stores are selected by their own variables in later tasks.
	BlobDir string
}

// FromEnv reads configuration from LCATD_-prefixed environment variables.
func FromEnv() (Config, error) {
	cfg := Config{
		ListenAddr: envOr("LCATD_LISTEN_ADDR", ":8080"),
		BlobDir:    os.Getenv("LCATD_BLOB_DIR"),
	}
	if cfg.ListenAddr == "" {
		return Config{}, fmt.Errorf("config: empty LCATD_LISTEN_ADDR")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
