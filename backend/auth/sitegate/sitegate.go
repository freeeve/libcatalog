// Package sitegate is a reusable login gate for a statically hosted site
// behind CloudFront: authorization code + PKCE against an OIDC issuer as a
// public client, id_token verification through auth/oidc (JWKS signature,
// issuer, audience, expiry), a ranked-role check, then CloudFront signed
// cookies (custom policy) that unlock every other path on the distribution.
// Unauthenticated visitors hit the distribution's 403 custom error page
// ({prefix}/gate), which bounces them into the flow and back to the page
// they asked for.
//
// The handler speaks plain net/http, so a Lambda deployment wraps it with
// backend/awslambda.Handler (Function URLs share the API Gateway v2 payload
// shape) and a container or VM deployment mounts it directly.
package sitegate

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/auth/oidc"
)

// Config describes one gated site.
type Config struct {
	// Issuer is the OIDC issuer URL; the gate is a public client of it.
	Issuer string
	// ClientID is the public OIDC client id; the id_token's aud must match.
	ClientID string
	// SiteDomain is the site's host, e.g. "opac.example.org" -- it scopes
	// the CloudFront cookies and forms the redirect_uri
	// (https://{SiteDomain}{PathPrefix}/callback).
	SiteDomain string
	// KeyPairID is the CloudFront public key id (K...) of the signer whose
	// public half is registered in the distribution's trusted key group.
	KeyPairID string
	// PrivateKeyPEM is the CloudFront signer's RSA private key (PKCS#1 or
	// PKCS#8 PEM).
	PrivateKeyPEM string
	// MinRole is the lowest role admitted; higher-ranked roles pass too.
	// Default auth.RoleLibrarian.
	MinRole auth.Role
	// RoleClaim and RoleMap configure how the id_token's roles are read;
	// both pass through to oidc.Config.
	RoleClaim string
	RoleMap   map[string]auth.Role
	// JWKSURL overrides key discovery (passes through to oidc.Config).
	JWKSURL string
	// Scopes overrides the requested scopes. Default openid, email, profile.
	Scopes []string
	// SiteName titles the interstitial gate page. Default SiteDomain.
	SiteName string
	// PathPrefix is where the distribution routes the gate. Default "/_auth".
	PathPrefix string
	// SessionTTL bounds the signed cookies; revocation is expiry. Default 12h.
	SessionTTL time.Duration
	// HTTPClient overrides the client used against the issuer (tests).
	HTTPClient *http.Client
}

// Gate is the http.Handler serving {PathPrefix}/{login,callback,logout,gate}.
type Gate struct {
	cfg      Config
	verifier *oidc.Verifier
	signer   *rsa.PrivateKey
	client   *http.Client

	mu   sync.Mutex
	disc oidc.Discovery
}

// New validates cfg, parses the CloudFront signer key, and wires the
// id_token verifier (JWKS fetching is cached and auto-refreshed by
// auth/oidc). Endpoint discovery happens lazily on the first login, so an
// issuer outage does not fail construction.
func New(ctx context.Context, cfg Config) (*Gate, error) {
	if cfg.Issuer == "" || cfg.ClientID == "" || cfg.SiteDomain == "" || cfg.KeyPairID == "" || cfg.PrivateKeyPEM == "" {
		return nil, errors.New("sitegate: Issuer, ClientID, SiteDomain, KeyPairID and PrivateKeyPEM are required")
	}
	if cfg.MinRole == "" {
		cfg.MinRole = auth.RoleLibrarian
	}
	if !cfg.MinRole.Valid() {
		return nil, fmt.Errorf("sitegate: unknown MinRole %q", cfg.MinRole)
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{"openid", "email", "profile"}
	}
	if cfg.SiteName == "" {
		cfg.SiteName = cfg.SiteDomain
	}
	if cfg.PathPrefix == "" {
		cfg.PathPrefix = "/_auth"
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 12 * time.Hour
	}
	signer, err := parseRSAKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("sitegate: CloudFront signer key: %w", err)
	}
	verifier, err := oidc.New(ctx, oidc.Config{
		Issuer:     cfg.Issuer,
		JWKSURL:    cfg.JWKSURL,
		Audience:   cfg.ClientID,
		RoleClaim:  cfg.RoleClaim,
		RoleMap:    cfg.RoleMap,
		HTTPClient: cfg.HTTPClient,
	})
	if err != nil {
		return nil, err
	}
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Gate{cfg: cfg, verifier: verifier, signer: signer, client: client}, nil
}

// ServeHTTP routes the four gate paths; everything else is 404.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch strings.TrimSuffix(r.URL.Path, "/") {
	case g.cfg.PathPrefix + "/login":
		g.handleLogin(w, r)
	case g.cfg.PathPrefix + "/callback":
		g.handleCallback(w, r)
	case g.cfg.PathPrefix + "/logout":
		g.handleLogout(w)
	case g.cfg.PathPrefix + "/gate":
		g.handleGate(w)
	default:
		textError(w, http.StatusNotFound, "not found")
	}
}

var gatePage = template.Must(template.New("gate").Parse(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>{{.SiteName}} — sign in</title></head>
<body><p>Signing you in…</p>
<script>location.replace({{.LoginPath}}+"?return="+encodeURIComponent(location.pathname+location.search));</script>
<noscript><a href="{{.LoginPath}}">Sign in</a></noscript></body></html>`))

// handleGate serves the CloudFront 403 custom-error document. The browser's
// URL is still the page the visitor asked for (custom error responses do not
// redirect), so a script forwards it as the post-login return target.
func (g *Gate) handleGate(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_ = gatePage.Execute(w, map[string]string{
		"SiteName":  g.cfg.SiteName,
		"LoginPath": g.cfg.PathPrefix + "/login",
	})
}

// handleLogout expires the signed cookies and lands on the gate page, which
// immediately offers sign-in again.
func (g *Gate) handleLogout(w http.ResponseWriter) {
	for _, name := range []string{"CloudFront-Policy", "CloudFront-Signature", "CloudFront-Key-Pair-Id"} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: "", Domain: g.cfg.SiteDomain, Path: "/",
			MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
	}
	redirect(w, "https://"+g.cfg.SiteDomain+g.cfg.PathPrefix+"/gate")
}

func redirect(w http.ResponseWriter, location string) {
	w.Header().Set("Location", location)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusFound)
}

func textError(w http.ResponseWriter, code int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

// parseRSAKey accepts the CloudFront signer as PKCS#1 or PKCS#8 PEM.
func parseRSAKey(pemKey string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, errors.New("no PEM block")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("unparseable private key")
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return key, nil
}
