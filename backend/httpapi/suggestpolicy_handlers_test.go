package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
)

func decodePolicy(t *testing.T, body string) suggest.Policy {
	t.Helper()
	var p suggest.Policy
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decode policy: %v (body %q)", err, body)
	}
	return p
}

// TestSuggestionPolicyReadWriteAndAudit covers the admin config surface
// : the default is off, a PUT normalizes and persists the policy and
// is audited, and both the admin GET and the public read reflect it.
func TestSuggestionPolicyReadWriteAndAudit(t *testing.T) {
	queue := suggest.New(store.NewMem(), nil, suggest.Caps{})
	h := New(Deps{Suggest: queue, Verifier: adminVerifier()})

	// Default: off.
	rec := request(t, h, http.MethodGet, "/v1/config/suggestions", "admin-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET default = %d %s", rec.Code, rec.Body.String())
	}
	if decodePolicy(t, rec.Body.String()).Enabled {
		t.Error("fresh deployment reports suggestions enabled, want off")
	}

	// Enable, with an allowlist and a free-text mode.
	rec = request(t, h, http.MethodPut, "/v1/config/suggestions", "admin-token", "", map[string]any{
		"enabled":  true,
		"schemes":  []string{"homosaurus", "", "homosaurus"},
		"freeText": "any",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT enable = %d %s", rec.Code, rec.Body.String())
	}
	saved := decodePolicy(t, rec.Body.String())
	if !saved.Enabled || saved.FreeText != suggest.FreeTextAny {
		t.Errorf("PUT returned %+v, want enabled any", saved)
	}
	if len(saved.Schemes) != 1 || saved.Schemes[0] != "homosaurus" {
		t.Errorf("schemes not deduped: %v", saved.Schemes)
	}

	// The admin GET and the public read both reflect it.
	rec = request(t, h, http.MethodGet, "/v1/config/suggestions", "admin-token", "", nil)
	if !decodePolicy(t, rec.Body.String()).Enabled {
		t.Error("admin GET does not reflect the enable")
	}
	rec = request(t, h, http.MethodGet, "/v1/suggestions/policy", "", "", nil)
	if rec.Code != http.StatusOK || !decodePolicy(t, rec.Body.String()).Enabled {
		t.Errorf("public policy read = %d %s", rec.Code, rec.Body.String())
	}

	// The write is audited with the actor and the resulting policy.
	entry, ok := auditByAction(t, queue)["SUGGESTION_POLICY_SET"]
	if !ok {
		t.Fatal("PUT wrote no SUGGESTION_POLICY_SET audit entry")
	}
	if entry.Actor != "admin@example.org" {
		t.Errorf("audit actor = %q", entry.Actor)
	}
	if !strings.Contains(entry.Note, "enabled") || !strings.Contains(entry.Note, "homosaurus") || !strings.Contains(entry.Note, "any") {
		t.Errorf("audit note = %q, want it to state the resulting policy", entry.Note)
	}
}

// TestSuggestionPolicyRejectsBadMode: an unknown free-text mode is a 400.
func TestSuggestionPolicyRejectsBadMode(t *testing.T) {
	queue := suggest.New(store.NewMem(), nil, suggest.Caps{})
	h := New(Deps{Suggest: queue, Verifier: adminVerifier()})
	rec := request(t, h, http.MethodPut, "/v1/config/suggestions", "admin-token", "", map[string]any{
		"enabled": true, "freeText": "whenever",
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT bad mode = %d %s, want 400", rec.Code, rec.Body.String())
	}
}

// TestSuggestionPolicyAdminGated: the config surface is admin-only; the public
// read is not.
func TestSuggestionPolicyAdminGated(t *testing.T) {
	queue := suggest.New(store.NewMem(), nil, suggest.Caps{})
	h := New(Deps{Suggest: queue, Verifier: adminVerifier()})
	if rec := request(t, h, http.MethodGet, "/v1/config/suggestions", "", "", nil); rec.Code == http.StatusOK {
		t.Errorf("unauthenticated admin GET = %d, want denied", rec.Code)
	}
	if rec := request(t, h, http.MethodPut, "/v1/config/suggestions", "", "", map[string]any{"enabled": true}); rec.Code == http.StatusOK {
		t.Errorf("unauthenticated admin PUT = %d, want denied", rec.Code)
	}
}
