package sitegate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/auth/oidc"
)

// flowCookie carries state, PKCE verifier, and return target between login
// and callback; it is scoped to the gate's path prefix and short-lived.
const flowCookie = "sitegate_flow"

// endpoints resolves (once, then cached for the process lifetime) the
// issuer's authorization and token endpoints via OIDC discovery. A failed
// discovery is not cached, so a transiently-down issuer retries.
func (g *Gate) endpoints(ctx context.Context) (oidc.Discovery, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.disc.AuthorizationEndpoint != "" {
		return g.disc, nil
	}
	disc, err := oidc.Discover(ctx, g.client, g.cfg.Issuer)
	if err != nil {
		return oidc.Discovery{}, err
	}
	if disc.AuthorizationEndpoint == "" || disc.TokenEndpoint == "" {
		return oidc.Discovery{}, errors.New("sitegate: discovery lacks authorization or token endpoint")
	}
	g.disc = disc
	return g.disc, nil
}

func (g *Gate) redirectURI() string {
	return "https://" + g.cfg.SiteDomain + g.cfg.PathPrefix + "/callback"
}

// handleLogin opens the code+PKCE flow: state and verifier ride in a
// short-lived HttpOnly cookie scoped to the gate prefix, the challenge goes
// to the issuer.
func (g *Gate) handleLogin(w http.ResponseWriter, r *http.Request) {
	disc, err := g.endpoints(r.Context())
	if err != nil {
		textError(w, http.StatusBadGateway, "auth provider unreachable")
		return
	}
	state := randToken()
	verifier := randToken()
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	flow, _ := json.Marshal(map[string]string{
		"state": state, "verifier": verifier, "return": returnPath(r.URL.Query().Get("return")),
	})
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie, Value: base64.RawURLEncoding.EncodeToString(flow),
		Path: g.cfg.PathPrefix, MaxAge: 600, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})

	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {g.cfg.ClientID},
		"redirect_uri":          {g.redirectURI()},
		"scope":                 {strings.Join(g.cfg.Scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	redirect(w, disc.AuthorizationEndpoint+"?"+q.Encode())
}

// handleCallback finishes the flow: state check against the flow cookie,
// code exchange with the PKCE verifier, id_token verification (signature via
// JWKS, issuer, audience, expiry -- auth/oidc), role gate, then CloudFront
// signed cookies and a bounce back to the page the visitor asked for.
func (g *Gate) handleCallback(w http.ResponseWriter, r *http.Request) {
	var flow struct{ State, Verifier, Return string }
	if c, err := r.Cookie(flowCookie); err == nil {
		if raw, err := base64.RawURLEncoding.DecodeString(c.Value); err == nil {
			_ = json.Unmarshal(raw, &flow)
		}
	}
	if flow.State == "" || r.URL.Query().Get("state") != flow.State {
		textError(w, http.StatusBadRequest, "login flow expired -- go back and retry")
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		textError(w, http.StatusBadRequest, "missing code")
		return
	}

	disc, err := g.endpoints(r.Context())
	if err != nil {
		textError(w, http.StatusBadGateway, "auth provider unreachable")
		return
	}
	idToken, err := g.exchangeCode(r.Context(), disc.TokenEndpoint, code, flow.Verifier)
	if err != nil {
		textError(w, http.StatusUnauthorized, "token exchange rejected")
		return
	}
	id, err := g.verifier.Verify(r.Context(), idToken)
	if err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			textError(w, http.StatusForbidden, g.forbiddenMessage(""))
			return
		}
		textError(w, http.StatusUnauthorized, "invalid token")
		return
	}
	if !id.Has(g.cfg.MinRole) {
		textError(w, http.StatusForbidden, g.forbiddenMessage(id.Email))
		return
	}

	for _, c := range g.signedCookies(time.Now().Add(g.cfg.SessionTTL)) {
		http.SetCookie(w, c)
	}
	// Expire the flow cookie alongside setting the session.
	http.SetCookie(w, &http.Cookie{
		Name: flowCookie, Value: "", Path: g.cfg.PathPrefix,
		MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	redirect(w, flow.Return)
}

// exchangeCode redeems the authorization code at the token endpoint as a
// public client (PKCE verifier, no client secret) and returns the id_token.
func (g *Gate) exchangeCode(ctx context.Context, tokenURL, code, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {g.redirectURI()},
		"client_id":     {g.cfg.ClientID},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := g.client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var tok struct {
		IDToken string `json:"id_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&tok); err != nil || res.StatusCode != http.StatusOK || tok.IDToken == "" {
		return "", fmt.Errorf("token endpoint status %d", res.StatusCode)
	}
	return tok.IDToken, nil
}

func (g *Gate) forbiddenMessage(email string) string {
	who := "signed in"
	if email != "" {
		who = "signed in as " + email
	}
	return fmt.Sprintf("%s, but this site is limited to %s accounts", who, g.cfg.MinRole)
}

// returnPath admits only path-absolute return targets: no other hosts (open
// redirect), no protocol-relative "//" or backslash-smuggled "/\" forms.
func returnPath(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, `/\`) {
		return "/"
	}
	return p
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
