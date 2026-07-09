package sitegate

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // verifying the CloudFront RSA-SHA1 signature
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"
)

// issuer is a fake OIDC provider: discovery, JWKS, and a token endpoint that
// checks the PKCE verifier and returns a signed id_token.
type issuer struct {
	t      *testing.T
	key    jwk.Key
	pub    jwk.Set
	server *httptest.Server
	// role lands in the next id_token's role claim.
	role any
	// wantVerifier, when set, must match the exchanged code_verifier.
	wantVerifier  string
	lastTokenForm url.Values
}

func newIssuer(t *testing.T) *issuer {
	t.Helper()
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
	iss := &issuer{t: t, key: key, pub: pub, role: "librarian"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": iss.server.URL + "/authorize",
			"token_endpoint":         iss.server.URL + "/token",
			"jwks_uri":               iss.server.URL + "/keys",
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(iss.pub)
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		iss.lastTokenForm = r.PostForm
		if iss.wantVerifier != "" && r.PostForm.Get("code_verifier") != iss.wantVerifier {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": iss.idToken()})
	})
	iss.server = httptest.NewServer(mux)
	t.Cleanup(iss.server.Close)
	return iss
}

// idToken mints an id_token as the issuer would after a successful exchange.
func (iss *issuer) idToken() string {
	tok, err := jwt.NewBuilder().
		Issuer(iss.server.URL).
		Subject("user-1").
		Audience([]string{"gatedsite"}).
		IssuedAt(time.Now()).
		Expiration(time.Now().Add(time.Hour)).
		Claim("email", "eve@example.org").
		Claim("role", iss.role).
		Build()
	if err != nil {
		iss.t.Fatal(err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256, iss.key))
	if err != nil {
		iss.t.Fatal(err)
	}
	return string(signed)
}

// cfKey generates the CloudFront signer used by the gate under test.
func cfKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	return key, pemKey
}

func newGate(t *testing.T, iss *issuer, pemKey string) *Gate {
	t.Helper()
	g, err := New(context.Background(), Config{
		Issuer:        iss.server.URL,
		ClientID:      "gatedsite",
		SiteDomain:    "opac.example.org",
		KeyPairID:     "KTEST",
		PrivateKeyPEM: pemKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// doLogin runs GET /_auth/login and returns the authorize redirect and the
// decoded flow cookie.
func doLogin(t *testing.T, g *Gate, ret string) (*url.URL, *http.Cookie, struct{ State, Verifier, Return string }) {
	t.Helper()
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_auth/login?return="+url.QueryEscape(ret), nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("login status = %d, body %s", rec.Code, rec.Body)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == flowCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no flow cookie set")
	}
	var flow struct{ State, Verifier, Return string }
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || json.Unmarshal(raw, &flow) != nil {
		t.Fatalf("undecodable flow cookie %q", cookie.Value)
	}
	return loc, cookie, flow
}

func TestLoginRedirect(t *testing.T) {
	iss := newIssuer(t)
	_, pemKey := cfKey(t)
	g := newGate(t, iss, pemKey)

	loc, cookie, flow := doLogin(t, g, "/works?page=2")
	if got := loc.Scheme + "://" + loc.Host + loc.Path; got != iss.server.URL+"/authorize" {
		t.Errorf("authorize URL = %s", got)
	}
	q := loc.Query()
	if q.Get("client_id") != "gatedsite" || q.Get("response_type") != "code" ||
		q.Get("code_challenge_method") != "S256" || q.Get("state") != flow.State {
		t.Errorf("authorize query = %v", q)
	}
	if q.Get("redirect_uri") != "https://opac.example.org/_auth/callback" {
		t.Errorf("redirect_uri = %s", q.Get("redirect_uri"))
	}
	sum := sha256.Sum256([]byte(flow.Verifier))
	if q.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Error("code_challenge is not S256(verifier)")
	}
	if flow.Return != "/works?page=2" {
		t.Errorf("return = %q", flow.Return)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.Path != "/_auth" {
		t.Errorf("flow cookie attributes: %+v", cookie)
	}
}

// callback replays the issuer's redirect back into the gate.
func callback(g *Gate, cookie *http.Cookie, state string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/_auth/callback?code=authcode&state="+url.QueryEscape(state), nil)
	if cookie != nil {
		req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	g.ServeHTTP(rec, req)
	return rec
}

func TestFullFlow(t *testing.T) {
	iss := newIssuer(t)
	key, pemKey := cfKey(t)
	g := newGate(t, iss, pemKey)

	_, cookie, flow := doLogin(t, g, "/works")
	iss.wantVerifier = flow.Verifier
	rec := callback(g, cookie, flow.State)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/works" {
		t.Fatalf("callback = %d -> %q, body %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	if iss.lastTokenForm.Get("grant_type") != "authorization_code" || iss.lastTokenForm.Get("client_secret") != "" {
		t.Errorf("token form = %v", iss.lastTokenForm)
	}

	cookies := map[string]*http.Cookie{}
	for _, c := range rec.Result().Cookies() {
		cookies[c.Name] = c
	}
	for _, name := range []string{"CloudFront-Policy", "CloudFront-Signature", "CloudFront-Key-Pair-Id"} {
		c := cookies[name]
		if c == nil {
			t.Fatalf("missing %s cookie", name)
		}
		if !c.HttpOnly || !c.Secure || c.Domain != "opac.example.org" || c.Path != "/" || c.MaxAge <= 0 {
			t.Errorf("%s attributes: %+v", name, c)
		}
	}
	if fc := cookies[flowCookie]; fc == nil || fc.MaxAge >= 0 {
		t.Error("flow cookie not expired")
	}
	policy, err := cfDecode(cookies["CloudFront-Policy"].Value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(policy), `"Resource":"https://opac.example.org/*"`) {
		t.Errorf("policy = %s", policy)
	}
	sig, err := cfDecode(cookies["CloudFront-Signature"].Value)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha1.Sum(policy) //nolint:gosec
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA1, sum[:], sig); err != nil {
		t.Errorf("signature does not verify: %v", err)
	}
	if cookies["CloudFront-Key-Pair-Id"].Value != "KTEST" {
		t.Errorf("key pair id = %s", cookies["CloudFront-Key-Pair-Id"].Value)
	}
}

// cfDecode reverses cfEncode for the test.
func cfDecode(s string) ([]byte, error) {
	r := strings.NewReplacer("-", "+", "_", "=", "~", "/")
	return base64.StdEncoding.DecodeString(r.Replace(s))
}

func TestCallbackStateChecks(t *testing.T) {
	iss := newIssuer(t)
	_, pemKey := cfKey(t)
	g := newGate(t, iss, pemKey)
	_, cookie, flow := doLogin(t, g, "/")

	if rec := callback(g, nil, flow.State); rec.Code != http.StatusBadRequest {
		t.Errorf("no flow cookie: %d", rec.Code)
	}
	if rec := callback(g, cookie, "forged-state"); rec.Code != http.StatusBadRequest {
		t.Errorf("state mismatch: %d", rec.Code)
	}
}

func TestRoleGate(t *testing.T) {
	for name, role := range map[string]any{
		"below minimum": "patron",
		"unknown role":  "stranger",
		"missing role":  nil,
	} {
		t.Run(name, func(t *testing.T) {
			iss := newIssuer(t)
			iss.role = role
			_, pemKey := cfKey(t)
			g := newGate(t, iss, pemKey)
			_, cookie, flow := doLogin(t, g, "/")
			rec := callback(g, cookie, flow.State)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
			}
			for _, c := range rec.Result().Cookies() {
				if strings.HasPrefix(c.Name, "CloudFront-") {
					t.Errorf("forbidden response set %s", c.Name)
				}
			}
		})
	}
	// admin outranks the default librarian minimum.
	iss := newIssuer(t)
	iss.role = "admin"
	_, pemKey := cfKey(t)
	g := newGate(t, iss, pemKey)
	_, cookie, flow := doLogin(t, g, "/")
	if rec := callback(g, cookie, flow.State); rec.Code != http.StatusFound {
		t.Errorf("admin rejected: %d, body %s", rec.Code, rec.Body)
	}
}

func TestPKCEVerifierRejected(t *testing.T) {
	iss := newIssuer(t)
	iss.wantVerifier = "a-different-verifier"
	_, pemKey := cfKey(t)
	g := newGate(t, iss, pemKey)
	_, cookie, flow := doLogin(t, g, "/")
	if rec := callback(g, cookie, flow.State); rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestGatePage(t *testing.T) {
	iss := newIssuer(t)
	_, pemKey := cfKey(t)
	g := newGate(t, iss, pemKey)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_auth/gate", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, "/_auth/login") || !strings.Contains(body, "opac.example.org") {
		t.Errorf("gate page = %s", body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
}

func TestLogout(t *testing.T) {
	iss := newIssuer(t)
	_, pemKey := cfKey(t)
	g := newGate(t, iss, pemKey)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_auth/logout", nil))
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://opac.example.org/_auth/gate" {
		t.Errorf("logout = %d -> %q", rec.Code, rec.Header().Get("Location"))
	}
	expired := 0
	for _, c := range rec.Result().Cookies() {
		if strings.HasPrefix(c.Name, "CloudFront-") && c.MaxAge < 0 {
			expired++
		}
	}
	if expired != 3 {
		t.Errorf("expired %d of 3 CloudFront cookies", expired)
	}
}

func TestUnknownPath(t *testing.T) {
	iss := newIssuer(t)
	_, pemKey := cfKey(t)
	g := newGate(t, iss, pemKey)
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_auth/other", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestConfigValidation(t *testing.T) {
	iss := newIssuer(t)
	_, pemKey := cfKey(t)
	base := Config{
		Issuer: iss.server.URL, ClientID: "gatedsite", SiteDomain: "opac.example.org",
		KeyPairID: "KTEST", PrivateKeyPEM: pemKey,
	}
	for name, mutate := range map[string]func(*Config){
		"missing issuer":  func(c *Config) { c.Issuer = "" },
		"missing client":  func(c *Config) { c.ClientID = "" },
		"missing domain":  func(c *Config) { c.SiteDomain = "" },
		"missing key id":  func(c *Config) { c.KeyPairID = "" },
		"missing key":     func(c *Config) { c.PrivateKeyPEM = "" },
		"garbage key":     func(c *Config) { c.PrivateKeyPEM = "not pem" },
		"unknown minrole": func(c *Config) { c.MinRole = "emperor" },
	} {
		cfg := base
		mutate(&cfg)
		if _, err := New(context.Background(), cfg); err == nil {
			t.Errorf("%s: New accepted the config", name)
		}
	}
	if _, err := New(context.Background(), base); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestReturnPath(t *testing.T) {
	for in, want := range map[string]string{
		"/works?page=2":     "/works?page=2",
		"":                  "/",
		"/":                 "/",
		"//evil.example":    "/",
		`/\evil.example`:    "/",
		"https://evil.test": "/",
		"relative/path":     "/",
	} {
		if got := returnPath(in); got != want {
			t.Errorf("returnPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzReturnPath asserts the sanitizer invariant: whatever comes in, the
// result stays on the site (path-absolute, never protocol-relative or
// backslash-smuggled).
func FuzzReturnPath(f *testing.F) {
	for _, seed := range []string{"/works", "//evil", `/\evil`, "https://x", "", "/a?b=c#d"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, in string) {
		out := returnPath(in)
		if !strings.HasPrefix(out, "/") || strings.HasPrefix(out, "//") || strings.HasPrefix(out, `/\`) {
			t.Errorf("returnPath(%q) = %q escapes the site", in, out)
		}
		if out != "/" && out != in {
			t.Errorf("returnPath(%q) = %q mangled a value it accepted", in, out)
		}
	})
}
