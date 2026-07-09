package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/auth/sitegate"
)

// fileConfig is the config file shape: one [sitegate] table carrying the
// non-secret gate settings.
type fileConfig struct {
	Sitegate tableConfig `toml:"sitegate"`
}

// tableConfig mirrors sitegate.Config field-for-field under kebab-case toml
// keys; session-ttl is a Go duration string (e.g. "12h").
type tableConfig struct {
	Issuer     string            `toml:"issuer"`
	ClientID   string            `toml:"client-id"`
	SiteDomain string            `toml:"site-domain"`
	KeyPairID  string            `toml:"key-pair-id"`
	SiteName   string            `toml:"site-name"`
	MinRole    string            `toml:"min-role"`
	RoleClaim  string            `toml:"role-claim"`
	RoleMap    map[string]string `toml:"role-map"`
	JWKSURL    string            `toml:"jwks-url"`
	Scopes     []string          `toml:"scopes"`
	PathPrefix string            `toml:"path-prefix"`
	SessionTTL string            `toml:"session-ttl"`
}

// loadConfig reads the [sitegate] table from path and joins it with the
// CloudFront signer key from the environment; sitegate.New validates the
// combined result (required fields, key parse, min-role).
func loadConfig(path, keyEnv string) (sitegate.Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return sitegate.Config{}, err
	}
	var fc fileConfig
	if err := toml.Unmarshal(b, &fc); err != nil {
		return sitegate.Config{}, fmt.Errorf("%s: %w", path, err)
	}
	t := fc.Sitegate
	cfg := sitegate.Config{
		Issuer:        t.Issuer,
		ClientID:      t.ClientID,
		SiteDomain:    t.SiteDomain,
		KeyPairID:     t.KeyPairID,
		PrivateKeyPEM: decodeKeyEnv(keyEnv),
		MinRole:       auth.Role(t.MinRole),
		RoleClaim:     t.RoleClaim,
		JWKSURL:       t.JWKSURL,
		Scopes:        t.Scopes,
		SiteName:      t.SiteName,
		PathPrefix:    t.PathPrefix,
	}
	if len(t.RoleMap) > 0 {
		cfg.RoleMap = map[string]auth.Role{}
		for issuerRole, gateRole := range t.RoleMap {
			r := auth.Role(gateRole)
			if !r.Valid() {
				return sitegate.Config{}, fmt.Errorf("%s: role-map: %q maps to unknown role %q", path, issuerRole, gateRole)
			}
			cfg.RoleMap[issuerRole] = r
		}
	}
	if t.SessionTTL != "" {
		ttl, err := time.ParseDuration(t.SessionTTL)
		if err != nil {
			return sitegate.Config{}, fmt.Errorf("%s: session-ttl: %w", path, err)
		}
		cfg.SessionTTL = ttl
	}
	return cfg, nil
}

// decodeKeyEnv accepts the signer key as raw PEM or base64 of the PEM --
// raw PEM in an env var is a quoting fight, so deploy scripts base64 it.
func decodeKeyEnv(v string) string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" || strings.HasPrefix(trimmed, "-----BEGIN") {
		return v
	}
	if raw, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
		return string(raw)
	}
	return v
}
