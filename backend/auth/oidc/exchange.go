package oidc

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// ExchangeConfig wires the PKCE token-exchange proxy: the SPA is a public
// OIDC client, so the confidential client secret lives only server-side and
// the SPA exchanges its authorization code (and rotates refresh tokens)
// through this endpoint instead of calling the issuer directly -- the qllpoc
// pattern, which also sidesteps issuer CORS.
type ExchangeConfig struct {
	// TokenEndpoint is the issuer's token URL. Empty = resolve via OIDC
	// discovery at first use.
	TokenEndpoint string
	// Issuer is used for discovery when TokenEndpoint is empty.
	Issuer string
	// ClientID and ClientSecret are the confidential client credentials
	// added to every proxied request. An empty secret disables the proxy
	// (503) until deployment configuration provides one.
	ClientID     string
	ClientSecret string
	// HTTPClient overrides the upstream client (tests).
	HTTPClient *http.Client
}

// allowedGrants are the only grant types the proxy forwards.
var allowedGrants = map[string]bool{"authorization_code": true, "refresh_token": true}

// ExchangeHandler returns the POST /v1/auth/exchange handler. It accepts
// form-encoded token requests, injects the confidential credentials, forwards
// to the issuer's token endpoint, and relays the JSON response verbatim.
func ExchangeHandler(cfg ExchangeConfig) http.Handler {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	// Discovery resolves once per handler, like the JWKS cache: without this
	// every exchange pays an extra upstream round trip when TokenEndpoint is
	// left to discovery, which is how appdeps builds it. A failed
	// discovery is not cached, so a transiently-down issuer retries.
	var epMu sync.Mutex
	cachedEndpoint := ""
	resolveEndpoint := func(ctx context.Context) (string, error) {
		epMu.Lock()
		defer epMu.Unlock()
		if cachedEndpoint != "" {
			return cachedEndpoint, nil
		}
		ep, err := tokenEndpoint(ctx, client, cfg)
		if err != nil {
			return "", err
		}
		cachedEndpoint = ep
		return ep, nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.ClientSecret == "" {
			http.Error(w, `{"error":"exchange not configured"}`, http.StatusServiceUnavailable)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, `{"error":"bad form"}`, http.StatusBadRequest)
			return
		}
		grant := r.PostForm.Get("grant_type")
		if !allowedGrants[grant] {
			http.Error(w, `{"error":"unsupported grant_type"}`, http.StatusBadRequest)
			return
		}
		endpoint, err := resolveEndpoint(r.Context())
		if err != nil {
			http.Error(w, `{"error":"issuer unavailable"}`, http.StatusBadGateway)
			return
		}
		form := url.Values{}
		for key, vals := range r.PostForm {
			// The proxy owns the client credentials; never forward
			// caller-supplied ones.
			if key == "client_secret" || key == "client_id" {
				continue
			}
			form[key] = vals
		}
		form.Set("client_id", cfg.ClientID)
		form.Set("client_secret", cfg.ClientSecret)
		upstream, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint,
			strings.NewReader(form.Encode()))
		if err != nil {
			http.Error(w, `{"error":"issuer unavailable"}`, http.StatusBadGateway)
			return
		}
		upstream.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(upstream)
		if err != nil {
			http.Error(w, `{"error":"issuer unavailable"}`, http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}

func tokenEndpoint(ctx context.Context, client *http.Client, cfg ExchangeConfig) (string, error) {
	if cfg.TokenEndpoint != "" {
		return cfg.TokenEndpoint, nil
	}
	disc, err := Discover(ctx, client, cfg.Issuer)
	if err != nil {
		return "", err
	}
	return disc.TokenEndpoint, nil
}
