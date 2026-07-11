// Package local is the backend's built-in user management -- the auth path
// for deployments without an external IdP, coexisting with auth/oidc behind
// auth.Multi. Users live in the document store (argon2id password hashes);
// access tokens are short-lived Ed25519-signed JWTs from the service's own
// issuer; refresh tokens are opaque, stored hashed with TTL, and rotate on
// every use.
package local

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwa"
	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jwt"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
)

// ErrBadCredentials covers unknown user and wrong password identically, so
// login responses cannot be used to probe for accounts.
var ErrBadCredentials = errors.New("local: bad credentials")

// ErrRateLimited reports too many recent login failures for the account.
var ErrRateLimited = errors.New("local: rate limited")

const (
	defaultAccessTTL  = 15 * time.Minute
	defaultRefreshTTL = 30 * 24 * time.Hour
	// loginFailureCap is the per-account failed-login cap per hour window.
	// The counter is keyed by account, not caller, which an attacker who
	// knows a staff email can use to lock that account out for the rest of
	// the hour window -- an accepted trade-off for this deployment's size
	//: caller-keyed limits belong at the edge proxy, and a
	// locked window self-clears via the counter TTL.
	loginFailureCap = 10
	// rateWindowTTL clears stale failure-counter windows from the store.
	rateWindowTTL = 2 * time.Hour
)

// dummyPasswordHash is verified against on the unknown-user login path so
// that path costs the same argon2id work as a wrong password for a real
// account -- otherwise response latency is an account-existence oracle
// . PHC string for an unguessable throwaway password, using the
// same parameters as hashPassword.
const dummyPasswordHash = "$argon2id$v=19$m=65536,t=1,p=4$bGNhdC1kdW1teS1zYWx0IQ$M+w1/CTq7g1obR5MFtN+b4yAuc4ZjzWg3//Y+l8ELpQ"

// Service implements built-in users and auth.TokenVerifier for its own
// issued tokens.
type Service struct {
	store      store.Store
	key        ed25519.PrivateKey
	pub        jwk.Key
	issuer     string
	accessTTL  time.Duration
	refreshTTL time.Duration
	now        func() time.Time
}

// New wires the service. issuer must be unique among the deployment's
// configured issuers (auth.Multi dispatches on it); key signs access tokens.
func New(st store.Store, key ed25519.PrivateKey, issuer string) (*Service, error) {
	if issuer == "" {
		return nil, errors.New("local: empty issuer")
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("local: bad signing key size")
	}
	pub, err := jwk.FromRaw(key.Public())
	if err != nil {
		return nil, fmt.Errorf("local: public key: %w", err)
	}
	return &Service{
		store:      st,
		key:        key,
		pub:        pub,
		issuer:     issuer,
		accessTTL:  defaultAccessTTL,
		refreshTTL: defaultRefreshTTL,
		now:        time.Now,
	}, nil
}

// SetClock overrides the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Issuer returns the service's issuer string for auth.Multi registration.
func (s *Service) Issuer() string { return s.issuer }

// Tokens is a successful login/refresh result.
type Tokens struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
}

// Login checks credentials and issues tokens. Failures count toward a
// per-account hourly cap.
func (s *Service) Login(ctx context.Context, email, password string) (Tokens, error) {
	email = normalizeEmail(email)
	if email == "" || password == "" {
		return Tokens{}, ErrBadCredentials
	}
	window := s.now().UTC().Format("2006-01-02T15")
	rateKey := store.Key{PK: "RATE#LOGIN#" + email, SK: "HOUR#" + window}
	// The zero-delta read still creates the window item, so it carries the
	// same TTL as a failure bump -- without one, every clean login would
	// leave a counter row behind forever.
	failures, err := s.store.Increment(ctx, rateKey, 0, s.now().Add(rateWindowTTL))
	if err == nil && failures >= loginFailureCap {
		return Tokens{}, ErrRateLimited
	}
	u, err := s.getUser(ctx, email)
	if err != nil {
		// Unknown account: burn the same argon2id work a wrong password
		// costs, so latency cannot probe which emails exist.
		_, _ = verifyPassword(password, dummyPasswordHash)
		s.recordFailure(ctx, rateKey)
		return Tokens{}, ErrBadCredentials
	}
	ok, err := verifyPassword(password, u.PasswordHash)
	if err != nil || !ok {
		s.recordFailure(ctx, rateKey)
		return Tokens{}, ErrBadCredentials
	}
	return s.issue(ctx, u)
}

// recordFailure bumps the account's failed-login counter for the current
// hour window; the TTL clears stale windows.
func (s *Service) recordFailure(ctx context.Context, key store.Key) {
	_, _ = s.store.Increment(ctx, key, 1, s.now().Add(rateWindowTTL))
}

// Refresh rotates a refresh token: the presented token is retired and a new
// pair is issued. A reused (already-rotated) token fails.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Tokens, error) {
	key := refreshKey(refreshToken)
	rec, err := s.store.Get(ctx, key)
	if err != nil {
		return Tokens{}, ErrBadCredentials
	}
	var claim refreshClaim
	if err := json.Unmarshal(rec.Data, &claim); err != nil {
		return Tokens{}, ErrBadCredentials
	}
	// Rotate: delete conditionally so two concurrent refreshes cannot both
	// succeed with the same token.
	if err := s.store.Delete(ctx, rec, store.CondIfVersion); err != nil {
		return Tokens{}, ErrBadCredentials
	}
	u, err := s.getUser(ctx, claim.Email)
	if err != nil {
		return Tokens{}, ErrBadCredentials
	}
	return s.issue(ctx, u)
}

// Logout retires a refresh token.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	rec, err := s.store.Get(ctx, refreshKey(refreshToken))
	if err != nil {
		return nil // already gone; logout is idempotent
	}
	_ = s.store.Delete(ctx, rec, store.CondNone)
	return nil
}

type refreshClaim struct {
	Email string `json:"email"`
}

func refreshKey(token string) store.Key {
	sum := sha256.Sum256([]byte(token))
	return store.Key{PK: "REFRESH#" + hex.EncodeToString(sum[:]), SK: "TOKEN"}
}

func (s *Service) issue(ctx context.Context, u user) (Tokens, error) {
	now := s.now()
	roleNames := make([]string, len(u.Roles))
	for i, r := range u.Roles {
		roleNames[i] = string(r)
	}
	tok, err := jwt.NewBuilder().
		Issuer(s.issuer).
		Subject(u.Email).
		IssuedAt(now).
		Expiration(now.Add(s.accessTTL)).
		Claim("email", u.Email).
		Claim("name", u.Name).
		Claim("roles", roleNames).
		Build()
	if err != nil {
		return Tokens{}, err
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.EdDSA, s.key))
	if err != nil {
		return Tokens{}, err
	}
	refresh, err := randomToken()
	if err != nil {
		return Tokens{}, err
	}
	data, _ := json.Marshal(refreshClaim{Email: u.Email})
	rec := store.Record{Key: refreshKey(refresh), Data: data, ExpireAt: now.Add(s.refreshTTL)}
	if _, err := s.store.Put(ctx, rec, store.CondIfAbsent); err != nil {
		return Tokens{}, err
	}
	return Tokens{
		AccessToken:  string(signed),
		RefreshToken: refresh,
		ExpiresIn:    int(s.accessTTL.Seconds()),
	}, nil
}

// Verify implements auth.TokenVerifier for the service's own access tokens.
func (s *Service) Verify(ctx context.Context, raw string) (auth.Identity, error) {
	tok, err := jwt.ParseString(raw,
		jwt.WithKey(jwa.EdDSA, s.pub),
		jwt.WithValidate(true),
		jwt.WithIssuer(s.issuer),
		jwt.WithClock(jwt.ClockFunc(s.now)),
	)
	if err != nil {
		return auth.Identity{}, fmt.Errorf("%w: %v", auth.ErrUnauthorized, err)
	}
	var roles []auth.Role
	if claim, ok := tok.Get("roles"); ok {
		if list, ok := claim.([]any); ok {
			for _, item := range list {
				if name, ok := item.(string); ok && auth.Role(name).Valid() {
					roles = append(roles, auth.Role(name))
				}
			}
		}
	}
	if len(roles) == 0 {
		return auth.Identity{}, auth.ErrForbidden
	}
	id := auth.Identity{Subject: tok.Subject(), Roles: roles, Issuer: s.issuer}
	if email, ok := tok.Get("email"); ok {
		id.Email, _ = email.(string)
	}
	if name, ok := tok.Get("name"); ok {
		id.Name, _ = name.(string)
	}
	return id, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
