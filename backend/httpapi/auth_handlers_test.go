package httpapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/auth/local"
	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/store"
)

// otherIssuer is a stub second verifier proving Multi coexistence.
type otherIssuer struct{}

func (otherIssuer) Verify(ctx context.Context, raw string) (auth.Identity, error) {
	if raw == "sso-token" {
		return auth.Identity{Subject: "sso-user", Roles: []auth.Role{auth.RoleAdmin}, Issuer: "https://sso.example"}, nil
	}
	return auth.Identity{}, auth.ErrUnauthorized
}

func newAuthedAPI(t *testing.T) (http.Handler, *local.Service) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := local.New(store.NewMem(), key, "lcatd-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Bootstrap(t.Context(), "root@example.org:changeme123"); err != nil {
		t.Fatal(err)
	}
	verifier := auth.NewMulti(map[string]auth.TokenVerifier{
		"lcatd-test": svc,
	})
	// The SSO stub is not JWT-shaped, so exercise it through its own path
	// in TestMultiVerifierCoexistence rather than Multi.
	return New(Deps{Local: svc, Verifier: verifier}), svc
}

func doJSON(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestLoginFlowEndToEnd(t *testing.T) {
	h, _ := newAuthedAPI(t)

	// Bad credentials.
	rec := doJSON(t, h, http.MethodPost, "/v1/auth/login", "", map[string]string{"email": "root@example.org", "password": "wrong"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", rec.Code)
	}

	// Login.
	rec = doJSON(t, h, http.MethodPost, "/v1/auth/login", "", map[string]string{"email": "root@example.org", "password": "changeme123"})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	var tokens local.Tokens
	if err := json.Unmarshal(rec.Body.Bytes(), &tokens); err != nil || tokens.AccessToken == "" || tokens.RefreshToken == "" {
		t.Fatalf("tokens = %+v (%v)", tokens, err)
	}

	// Admin route requires the token.
	if rec := doJSON(t, h, http.MethodGet, "/v1/users", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("users without token: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodGet, "/v1/users", tokens.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("users with token: %d %s", rec.Code, rec.Body)
	}

	// Create a librarian; list shows both.
	rec = doJSON(t, h, http.MethodPost, "/v1/users", tokens.AccessToken, map[string]any{
		"email": "cat@example.org", "password": "password1", "roles": []string{"librarian"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", rec.Code, rec.Body)
	}
	rec = doJSON(t, h, http.MethodGet, "/v1/users", tokens.AccessToken, nil)
	var listing struct {
		Users []local.UserInfo `json:"users"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listing)
	if len(listing.Users) != 2 {
		t.Fatalf("users = %+v", listing.Users)
	}

	// The librarian's token cannot administer users.
	rec = doJSON(t, h, http.MethodPost, "/v1/auth/login", "", map[string]string{"email": "cat@example.org", "password": "password1"})
	var libTokens local.Tokens
	_ = json.Unmarshal(rec.Body.Bytes(), &libTokens)
	rec = doJSON(t, h, http.MethodGet, "/v1/users", libTokens.AccessToken, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("librarian on admin route: %d", rec.Code)
	}

	// Role update via path parameter.
	rec = doJSON(t, h, http.MethodPut, "/v1/users/cat@example.org/roles", tokens.AccessToken, map[string]any{"roles": []string{"admin"}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("set roles: %d %s", rec.Code, rec.Body)
	}

	// Refresh rotation via the API.
	rec = doJSON(t, h, http.MethodPost, "/v1/auth/refresh", "", map[string]string{"refreshToken": tokens.RefreshToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d", rec.Code)
	}
	var rotated local.Tokens
	_ = json.Unmarshal(rec.Body.Bytes(), &rotated)
	rec = doJSON(t, h, http.MethodPost, "/v1/auth/refresh", "", map[string]string{"refreshToken": tokens.RefreshToken})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("reused refresh: %d", rec.Code)
	}

	// Logout then refresh fails.
	rec = doJSON(t, h, http.MethodPost, "/v1/auth/logout", "", map[string]string{"refreshToken": rotated.RefreshToken})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodPost, "/v1/auth/refresh", "", map[string]string{"refreshToken": rotated.RefreshToken})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout: %d", rec.Code)
	}

	// Delete user. 200 with the (here empty) list of shared records the admin
	// inherited -- no Batch wired in this fixture, so nothing to reassign.
	rec = doJSON(t, h, http.MethodDelete, "/v1/users/cat@example.org", tokens.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
}

// TestDeleteUserReassignsSharedRecordsToAdmin is the fix: deleting a
// user hands their library-shared macros/templates to the deleting admin (a live
// custodian) and tells the admin which ones, instead of silently orphaning them.
func TestDeleteUserReassignsSharedRecordsToAdmin(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	svc, _ := local.New(store.NewMem(), key, "lcatd-test")
	if _, err := svc.Bootstrap(t.Context(), "root@example.org:changeme123"); err != nil {
		t.Fatal(err)
	}
	if err := svc.CreateUser(t.Context(), "cat@example.org", "Cat", "changeme123", []auth.Role{auth.RoleLibrarian}); err != nil {
		t.Fatal(err)
	}
	bsvc := &batch.Service{DB: store.NewMem()}
	// cat owns a library-shared macro and keeps a personal one.
	if _, err := bsvc.CreateMacro(t.Context(), batch.Macro{
		OwnedMeta: batch.OwnedMeta{Label: "Shared stamp", Shared: true},
		Ops:       []editor.Op{{Resource: "work", Path: "summary", Action: "set", Values: []editor.OpValue{{V: "x"}}}},
	}, "cat@example.org"); err != nil {
		t.Fatal(err)
	}
	if _, err := bsvc.CreateMacro(t.Context(), batch.Macro{
		OwnedMeta: batch.OwnedMeta{Label: "Private"},
		Ops:       []editor.Op{{Resource: "work", Path: "summary", Action: "set", Values: []editor.OpValue{{V: "y"}}}},
	}, "cat@example.org"); err != nil {
		t.Fatal(err)
	}

	verifier := auth.NewMulti(map[string]auth.TokenVerifier{"lcatd-test": svc})
	h := New(Deps{Local: svc, Verifier: verifier, Batch: bsvc})
	tokens, err := svc.Login(t.Context(), "root@example.org", "changeme123")
	if err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, h, http.MethodDelete, "/v1/users/cat@example.org", tokens.AccessToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Reassigned []batch.OwnedMeta `json:"reassigned"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Reassigned) != 1 || resp.Reassigned[0].Owner != "root@example.org" || resp.Reassigned[0].Label != "Shared stamp" {
		t.Fatalf("reassigned = %+v, want just the shared macro now owned by root", resp.Reassigned)
	}

	// root now owns the shared macro; the personal one is not reassigned.
	rootMacros, _ := bsvc.ListMacros(t.Context(), "root@example.org")
	var shared, personal int
	for _, m := range rootMacros {
		if m.Label == "Shared stamp" && m.Owner == "root@example.org" {
			shared++
		}
		if m.Label == "Private" {
			personal++
		}
	}
	if shared != 1 || personal != 0 {
		t.Fatalf("root macros after delete: shared=%d personal=%d, want shared=1 personal=0", shared, personal)
	}
}

func TestMultiVerifierCoexistence(t *testing.T) {
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	svc, _ := local.New(store.NewMem(), key, "lcatd-test")
	_, _ = svc.Bootstrap(t.Context(), "root@example.org:changeme123")
	tokens, err := svc.Login(t.Context(), "root@example.org", "changeme123")
	if err != nil {
		t.Fatal(err)
	}
	// Local tokens dispatch through Multi by their iss claim; the SSO stub
	// handles its own opaque token format directly.
	multi := auth.NewMulti(map[string]auth.TokenVerifier{"lcatd-test": svc})
	id, err := multi.Verify(t.Context(), tokens.AccessToken)
	if err != nil || !id.CanAdmin() || id.Issuer != "lcatd-test" {
		t.Fatalf("local via multi: %+v, %v", id, err)
	}
	sso := otherIssuer{}
	id, err = sso.Verify(t.Context(), "sso-token")
	if err != nil || id.Issuer != "https://sso.example" {
		t.Fatalf("sso: %+v, %v", id, err)
	}
}
