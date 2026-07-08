package oidc

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/freeeve/libcat/backend/auth"
)

// issuer is a fake OIDC issuer: an RSA key, a JWKS endpoint, discovery, and a
// token endpoint that records what the exchange proxy sends it.
type issuer struct {
	t      *testing.T
	key    jwk.Key
	pub    jwk.Set
	server *httptest.Server
	// lastTokenForm captures the most recent token-endpoint request form.
	lastTokenForm map[string][]string
}

func newIssuer(t *testing.T) *issuer {
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	key, err := jwk.FromRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	_ = key.Set(jwk.KeyIDKey, "test-key")
	_ = key.Set(jwk.AlgorithmKey, jwa.RS256)
	pubKey, err := key.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := jwk.NewSet()
	_ = pub.AddKey(pubKey)
	iss := &issuer{t: t, key: key, pub: pub}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"jwks_uri":       iss.server.URL + "/keys",
			"token_endpoint": iss.server.URL + "/token",
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(iss.pub)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		iss.lastTokenForm = r.PostForm
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "issued", "token_type": "Bearer"})
	})
	iss.server = httptest.NewServer(mux)
	t.Cleanup(iss.server.Close)
	return iss
}

// token signs a JWT with the issuer's key; claims override the valid
// defaults.
func (iss *issuer) token(claims map[string]any) string {
	b := jwt.NewBuilder().
		Issuer(iss.server.URL).
		Subject("user-1").
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour))
	for k, v := range claims {
		b = b.Claim(k, v)
	}
	tok, err := b.Build()
	if err != nil {
		iss.t.Fatal(err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, iss.key))
	if err != nil {
		iss.t.Fatal(err)
	}
	return string(signed)
}

func TestVerify(t *testing.T) {
	iss := newIssuer(t)
	v, err := New(t.Context(), Config{
		Issuer:      iss.server.URL,
		Audience:    iss.server.URL + "/userinfo",
		RoleMap:     map[string]auth.Role{"subject_moderator": auth.RoleModerator},
		ClaimEquals: map[string]string{"token_use": "access"},
		HTTPClient:  iss.server.Client(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	valid := map[string]any{
		"aud":       iss.server.URL + "/userinfo",
		"token_use": "access",
		"email":     "cat@example.org",
		"role":      "librarian",
	}
	id, err := v.Verify(t.Context(), iss.token(valid))
	if err != nil {
		t.Fatalf("valid token: %v", err)
	}
	if id.Email != "cat@example.org" || !id.CanPublish() || id.Issuer != iss.server.URL || id.Subject != "user-1" {
		t.Fatalf("identity = %+v", id)
	}

	with := func(mutate func(map[string]any)) string {
		claims := map[string]any{}
		for k, val := range valid {
			claims[k] = val
		}
		mutate(claims)
		return iss.token(claims)
	}
	rejected := map[string]string{
		"wrong audience":  with(func(c map[string]any) { c["aud"] = "https://elsewhere" }),
		"wrong token_use": with(func(c map[string]any) { c["token_use"] = "id" }),
		"garbage":         "not.a.jwt",
	}
	for name, tok := range rejected {
		if _, err := v.Verify(t.Context(), tok); !errors.Is(err, auth.ErrUnauthorized) {
			t.Errorf("%s: err = %v, want ErrUnauthorized", name, err)
		}
	}
	// Roles absent or unmapped -> forbidden.
	for _, role := range []any{nil, "janitor"} {
		tok := with(func(c map[string]any) {
			if role == nil {
				delete(c, "role")
			} else {
				c["role"] = role
			}
		})
		if _, err := v.Verify(t.Context(), tok); !errors.Is(err, auth.ErrForbidden) {
			t.Errorf("role %v: err = %v, want ErrForbidden", role, err)
		}
	}
	// Mapped issuer-specific role name and list-valued claims work.
	mapped := with(func(c map[string]any) { c["role"] = "subject_moderator" })
	if id, err := v.Verify(t.Context(), mapped); err != nil || !id.CanModerate() || id.CanPublish() {
		t.Fatalf("mapped role: %+v, %v", id, err)
	}
	listed := with(func(c map[string]any) { c["role"] = []string{"janitor", "admin"} })
	if id, err := v.Verify(t.Context(), listed); err != nil || !id.CanAdmin() {
		t.Fatalf("list role: %+v, %v", id, err)
	}
}

func TestVerifyExpired(t *testing.T) {
	iss := newIssuer(t)
	v, err := New(t.Context(), Config{Issuer: iss.server.URL, HTTPClient: iss.server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	tok, _ := jwt.NewBuilder().
		Issuer(iss.server.URL).Subject("u").
		Expiration(time.Now().Add(-time.Hour)).
		Claim("role", "librarian").Build()
	signed, _ := jwt.Sign(tok, jwt.WithKey(jwa.RS256, iss.key))
	if _, err := v.Verify(t.Context(), string(signed)); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("expired: err = %v, want ErrUnauthorized", err)
	}
}

func TestWrongKeyRejected(t *testing.T) {
	iss := newIssuer(t)
	other := newIssuer(t)
	v, err := New(t.Context(), Config{Issuer: iss.server.URL, HTTPClient: iss.server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	// Token claims iss's issuer but is signed with other's key.
	tok, _ := jwt.NewBuilder().
		Issuer(iss.server.URL).Subject("u").
		IssuedAt(time.Now()).Expiration(time.Now().Add(time.Hour)).
		Claim("role", "librarian").Build()
	signed, _ := jwt.Sign(tok, jwt.WithKey(jwa.RS256, other.key))
	if _, err := v.Verify(t.Context(), string(signed)); !errors.Is(err, auth.ErrUnauthorized) {
		t.Fatalf("wrong key: err = %v, want ErrUnauthorized", err)
	}
}

func TestExchangeHandler(t *testing.T) {
	iss := newIssuer(t)
	h := ExchangeHandler(ExchangeConfig{
		Issuer:       iss.server.URL,
		ClientID:     "tagreview",
		ClientSecret: "s3cret",
		HTTPClient:   iss.server.Client(),
	})
	form := "grant_type=authorization_code&code=abc&code_verifier=ver&redirect_uri=https%3A%2F%2Fapp%2Fcb&client_id=spoofed&client_secret=spoofed"
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/exchange", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["access_token"] != "issued" {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}
	got := iss.lastTokenForm
	if got["client_secret"][0] != "s3cret" || got["client_id"][0] != "tagreview" {
		t.Fatalf("credentials not injected/overridden: %v", got)
	}
	if got["code"][0] != "abc" || got["code_verifier"][0] != "ver" {
		t.Fatalf("PKCE fields not forwarded: %v", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("token response must be no-store")
	}

	// Unsupported grant rejected without an upstream call.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/auth/exchange", strings.NewReader("grant_type=password&username=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("password grant: %d, want 400", rec.Code)
	}

	// Unconfigured secret -> 503.
	unconfigured := ExchangeHandler(ExchangeConfig{Issuer: iss.server.URL})
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/auth/exchange", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	unconfigured.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured: %d, want 503", rec.Code)
	}
}

func TestJWKSFallbackWithoutDiscovery(t *testing.T) {
	// An issuer serving only {issuer}/jwks.json (no discovery document).
	var srv *httptest.Server
	mux := http.NewServeMux()
	raw, _ := rsa.GenerateKey(rand.Reader, 2048)
	key, _ := jwk.FromRaw(raw)
	_ = key.Set(jwk.KeyIDKey, "k")
	_ = key.Set(jwk.AlgorithmKey, jwa.RS256)
	pubKey, _ := key.PublicKey()
	set := jwk.NewSet()
	_ = set.AddKey(pubKey)
	mux.HandleFunc("GET /jwks.json", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(set)
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	v, err := New(t.Context(), Config{Issuer: srv.URL, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tok, _ := jwt.NewBuilder().
		Issuer(srv.URL).Subject("u").
		IssuedAt(time.Now()).Expiration(time.Now().Add(time.Hour)).
		Claim("email", "e@example.org").Claim("role", "admin").Build()
	signed, _ := jwt.Sign(tok, jwt.WithKey(jwa.RS256, key))
	id, err := v.Verify(t.Context(), string(signed))
	if err != nil || !id.CanAdmin() {
		t.Fatalf("fallback verify: %+v, %v", id, err)
	}
}
