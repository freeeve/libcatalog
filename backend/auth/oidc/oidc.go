// Package oidc verifies bearer tokens from an external OIDC issuer -- the
// pluggable-SSO half of the backend's auth (auth/local is the built-in half).
// Signature keys come from the issuer's JWKS (cached and auto-refreshed);
// role assignment is configurable (which claim, and how the issuer's role
// names map onto auth.Role), so any IdP that can mint a role-bearing access
// token plugs in without code.
package oidc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/freeeve/libcat/backend/auth"
)

// Config describes one external issuer.
type Config struct {
	// Issuer is the issuer URL; tokens' iss must match exactly.
	Issuer string
	// JWKSURL overrides key discovery. Empty = try OIDC discovery
	// ({issuer}/.well-known/openid-configuration), falling back to
	// {issuer}/jwks.json (the qllauthpoc convention).
	JWKSURL string
	// Audience, when set, must appear in the token's aud.
	Audience string
	// RoleClaim is the claim carrying the caller's role(s); string or list.
	// Default "role".
	RoleClaim string
	// RoleMap translates issuer role names to auth roles (e.g.
	// {"subject_moderator": auth.RoleModerator}). Issuer values that already
	// equal an auth.Role name map implicitly; anything else is ignored.
	RoleMap map[string]auth.Role
	// ClaimEquals lists extra exact-match claim requirements (e.g.
	// {"token_use": "access"} to reject id tokens).
	ClaimEquals map[string]string
	// EmailClaim is the claim carrying the caller's email. Default "email".
	EmailClaim string
	// HTTPClient overrides the client used for discovery and JWKS (tests).
	HTTPClient *http.Client
}

// Verifier implements auth.TokenVerifier for one external issuer.
type Verifier struct {
	cfg     Config
	jwks    *jwk.Cache
	jwksURL string
}

// New wires JWKS fetching for the issuer, resolving the JWKS URL via OIDC
// discovery when not configured.
func New(ctx context.Context, cfg Config) (*Verifier, error) {
	if cfg.Issuer == "" {
		return nil, fmt.Errorf("oidc: empty issuer")
	}
	if cfg.RoleClaim == "" {
		cfg.RoleClaim = "role"
	}
	if cfg.EmailClaim == "" {
		cfg.EmailClaim = "email"
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	jwksURL := cfg.JWKSURL
	if jwksURL == "" {
		if disc, err := Discover(ctx, client, cfg.Issuer); err == nil && disc.JWKSURI != "" {
			jwksURL = disc.JWKSURI
		} else {
			jwksURL = strings.TrimSuffix(cfg.Issuer, "/") + "/jwks.json"
		}
	}
	cache := jwk.NewCache(ctx)
	if err := cache.Register(jwksURL, jwk.WithMinRefreshInterval(15*time.Minute), jwk.WithHTTPClient(client)); err != nil {
		return nil, fmt.Errorf("oidc: register jwks: %w", err)
	}
	return &Verifier{cfg: cfg, jwks: cache, jwksURL: jwksURL}, nil
}

// Verify checks the token's signature against the issuer's JWKS and its
// issuer/audience/extra claims, then maps its role claim onto auth roles.
func (v *Verifier) Verify(ctx context.Context, raw string) (auth.Identity, error) {
	keys, err := v.jwks.Get(ctx, v.jwksURL)
	if err != nil {
		return auth.Identity{}, fmt.Errorf("oidc: fetch jwks: %w", err)
	}
	opts := []jwt.ParseOption{
		jwt.WithKeySet(keys),
		jwt.WithValidate(true),
		jwt.WithIssuer(v.cfg.Issuer),
	}
	if v.cfg.Audience != "" {
		opts = append(opts, jwt.WithAudience(v.cfg.Audience))
	}
	for claim, want := range v.cfg.ClaimEquals {
		opts = append(opts, jwt.WithClaimValue(claim, want))
	}
	tok, err := jwt.ParseString(raw, opts...)
	if err != nil {
		return auth.Identity{}, fmt.Errorf("%w: %v", auth.ErrUnauthorized, err)
	}
	roles := v.roles(tok)
	if len(roles) == 0 {
		return auth.Identity{}, auth.ErrForbidden
	}
	id := auth.Identity{
		Subject: tok.Subject(),
		Roles:   roles,
		Issuer:  v.cfg.Issuer,
	}
	if email, ok := tok.Get(v.cfg.EmailClaim); ok {
		id.Email, _ = email.(string)
	}
	if name, ok := tok.Get("name"); ok {
		id.Name, _ = name.(string)
	}
	if id.Subject == "" && id.Email == "" {
		return auth.Identity{}, auth.ErrUnauthorized
	}
	return id, nil
}

func (v *Verifier) roles(tok jwt.Token) []auth.Role {
	claim, ok := tok.Get(v.cfg.RoleClaim)
	if !ok {
		return nil
	}
	var names []string
	switch val := claim.(type) {
	case string:
		names = []string{val}
	case []any:
		for _, item := range val {
			if s, ok := item.(string); ok {
				names = append(names, s)
			}
		}
	}
	var roles []auth.Role
	for _, name := range names {
		if mapped, ok := v.cfg.RoleMap[name]; ok {
			roles = append(roles, mapped)
			continue
		}
		if r := auth.Role(name); r.Valid() {
			roles = append(roles, r)
		}
	}
	return roles
}

// Discovery is the subset of the issuer's OIDC discovery document the
// backend consumes; auth/sitegate additionally needs the interactive
// endpoints.
type Discovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// Discover fetches {issuer}/.well-known/openid-configuration. A nil client
// uses http.DefaultClient.
func Discover(ctx context.Context, client *http.Client, issuer string) (Discovery, error) {
	if client == nil {
		client = http.DefaultClient
	}
	url := strings.TrimSuffix(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Discovery{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Discovery{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Discovery{}, fmt.Errorf("oidc: discovery status %d", resp.StatusCode)
	}
	var d Discovery
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil {
		return Discovery{}, err
	}
	return d, nil
}
