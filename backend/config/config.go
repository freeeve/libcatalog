// Package config carries the backend's deployment configuration, read from
// the environment. It is deliberately SDK-free: cloud specifics (bucket names,
// table names, secret lookups) resolve to plain values before they reach the
// core, so the same configuration surface serves a container, a Lambda, and a
// laptop.
package config

import (
	"fmt"
	"os"
	"strings"
)

// Config is the resolved backend configuration.
type Config struct {
	// ListenAddr is the HTTP listen address for the standalone server
	// (ignored under Lambda). Default ":8080".
	ListenAddr string
	// BlobDir, when set, selects a local-directory grain store rooted there.
	// Cloud blob stores are selected by their own variables in later tasks.
	BlobDir string

	// LocalAuth enables built-in user management.
	LocalAuth bool
	// LocalIssuer is the built-in issuer string (must differ from any OIDC
	// issuer). Default "lcatd-local".
	LocalIssuer string
	// LocalSigningKey is the base64 (raw url or std) Ed25519 seed or private
	// key for access tokens. Empty = ephemeral key per boot (dev only:
	// restarts invalidate sessions).
	LocalSigningKey string
	// BootstrapAdmin is an "email:password" spec ensuring a first admin
	// exists at boot.
	BootstrapAdmin string

	// OIDCIssuer enables external-SSO verification when set.
	OIDCIssuer string
	// OIDCAudience, when set, must appear in tokens' aud.
	OIDCAudience string
	// OIDCRoleClaim is the role-bearing claim. Default "role".
	OIDCRoleClaim string
	// OIDCRoleMap maps issuer role names to backend roles
	// ("subject_moderator=moderator,staff=librarian").
	OIDCRoleMap map[string]string
	// OIDCClientID and OIDCClientSecret configure the PKCE token-exchange
	// proxy; an empty secret leaves the proxy returning 503.
	OIDCClientID     string
	OIDCClientSecret string

	// VocabSchemes lists the controlled vocabularies to load from the blob
	// store's authorities tree (comma-separated; empty = all found).
	VocabSchemes []string
	// AuthoritiesPrefix is the blob path prefix holding authority grains.
	// Default "data/authorities/".
	AuthoritiesPrefix string

	// AbuseSecret (>=16 bytes) keys IP pseudonymization and challenge
	// tokens; setting it enables the anonymous suggestion endpoints.
	AbuseSecret string

	// WebhookURL, when set, receives HMAC-signed grains-changed events after
	// each publish (WebhookSecret signs them).
	WebhookURL    string
	WebhookSecret string

	// Provider names the primary feed graph (CSV export projection).
	// Default "overdrive".
	Provider string

	// EnrichLocsh enables the id.loc.gov LCSH reconciliation source when set
	// to "queue" (moderated) or "direct" (auto-approve).
	EnrichLocsh string
}

// FromEnv reads configuration from LCATD_-prefixed environment variables.
func FromEnv() (Config, error) {
	cfg := Config{
		ListenAddr:        envOr("LCATD_LISTEN_ADDR", ":8080"),
		BlobDir:           os.Getenv("LCATD_BLOB_DIR"),
		LocalAuth:         os.Getenv("LCATD_LOCAL_AUTH") == "1" || os.Getenv("LCATD_LOCAL_AUTH") == "true",
		LocalIssuer:       envOr("LCATD_LOCAL_ISSUER", "lcatd-local"),
		LocalSigningKey:   os.Getenv("LCATD_LOCAL_SIGNING_KEY"),
		BootstrapAdmin:    os.Getenv("LCATD_BOOTSTRAP_ADMIN"),
		OIDCIssuer:        os.Getenv("LCATD_OIDC_ISSUER"),
		OIDCAudience:      os.Getenv("LCATD_OIDC_AUDIENCE"),
		OIDCRoleClaim:     envOr("LCATD_OIDC_ROLE_CLAIM", "role"),
		OIDCClientID:      os.Getenv("LCATD_OIDC_CLIENT_ID"),
		OIDCClientSecret:  os.Getenv("LCATD_OIDC_CLIENT_SECRET"),
		AuthoritiesPrefix: envOr("LCATD_AUTHORITIES_PREFIX", "data/authorities/"),
		AbuseSecret:       os.Getenv("LCATD_ABUSE_SECRET"),
		WebhookURL:        os.Getenv("LCATD_WEBHOOK_URL"),
		WebhookSecret:     os.Getenv("LCATD_WEBHOOK_SECRET"),
		Provider:          envOr("LCATD_PROVIDER", "overdrive"),
		EnrichLocsh:       os.Getenv("LCATD_ENRICH_LOCSH"),
	}
	if cfg.EnrichLocsh != "" && cfg.EnrichLocsh != "queue" && cfg.EnrichLocsh != "direct" {
		return Config{}, fmt.Errorf("config: LCATD_ENRICH_LOCSH must be queue or direct")
	}
	if raw := os.Getenv("LCATD_VOCAB_SCHEMES"); raw != "" {
		for s := range strings.SplitSeq(raw, ",") {
			if s = strings.TrimSpace(s); s != "" {
				cfg.VocabSchemes = append(cfg.VocabSchemes, s)
			}
		}
	}
	if cfg.ListenAddr == "" {
		return Config{}, fmt.Errorf("config: empty LCATD_LISTEN_ADDR")
	}
	if raw := os.Getenv("LCATD_OIDC_ROLE_MAP"); raw != "" {
		cfg.OIDCRoleMap = map[string]string{}
		for pair := range strings.SplitSeq(raw, ",") {
			from, to, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok || from == "" || to == "" {
				return Config{}, fmt.Errorf("config: bad LCATD_OIDC_ROLE_MAP entry %q", pair)
			}
			cfg.OIDCRoleMap[from] = to
		}
	}
	if cfg.LocalAuth && cfg.OIDCIssuer != "" && cfg.LocalIssuer == cfg.OIDCIssuer {
		return Config{}, fmt.Errorf("config: local and OIDC issuers must differ")
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
