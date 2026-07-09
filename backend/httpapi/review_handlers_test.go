package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/storage/blob"
)

// staffVerifier maps fixed tokens to staff identities.
type staffVerifier map[string]auth.Identity

func (v staffVerifier) Verify(ctx context.Context, raw string) (auth.Identity, error) {
	if id, ok := v[raw]; ok {
		return id, nil
	}
	return auth.Identity{}, auth.ErrUnauthorized
}

func newModerationAPI(t *testing.T) (http.Handler, *suggest.Service) {
	t.Helper()
	data, err := os.ReadFile("../vocab/testdata/authorities.nq")
	if err != nil {
		t.Fatal(err)
	}
	bs := blob.NewMem()
	_, _ = bs.Put(t.Context(), "a/x.nq", data, blob.PutOptions{})
	ix, err := vocab.Load(t.Context(), bs, "a/", nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := suggest.New(store.NewMem(), ix, suggest.Caps{})
	verifier := staffVerifier{
		"mod-token": {Email: "mod@example.org", Roles: []auth.Role{auth.RoleModerator}},
		"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}},
	}
	abuse, _ := suggest.NewAbuse([]byte("0123456789abcdef0123456789abcdef"))
	return New(Deps{Suggest: svc, Abuse: abuse, Vocab: ix, Verifier: verifier}), svc
}

func seed(t *testing.T, svc *suggest.Service, workID string) {
	t.Helper()
	_, err := svc.Submit(t.Context(), suggest.SubmitInput{
		WorkID: workID,
		Term:   vocab.TermRef{Scheme: "homosaurus", ID: transURI},
		Type:   suggest.TypeAdd, SupporterHash: "seed-hash", WorkTitle: "A Book",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestModerationFlow(t *testing.T) {
	h, svc := newModerationAPI(t)
	seed(t, svc, "wabc123def456")

	// Queue requires staff.
	if rec := doJSON(t, h, http.MethodGet, "/v1/queue", "", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous queue: %d", rec.Code)
	}
	rec := doJSON(t, h, http.MethodGet, "/v1/queue", "mod-token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", rec.Code, rec.Body)
	}
	var page suggest.QueuePage
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) != 1 {
		t.Fatalf("queue = %+v", page)
	}

	// Moderator approves but cannot publish.
	review := map[string]any{
		"decisions": []map[string]any{{
			"workId": "wabc123def456",
			"term":   map[string]string{"scheme": "homosaurus", "id": transURI},
			"type":   "ADD", "approve": true,
		}},
		"publish": true,
	}
	rec = doJSON(t, h, http.MethodPost, "/v1/review", "mod-token", review)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("moderator publish: %d, want 403", rec.Code)
	}
	review["publish"] = false
	rec = doJSON(t, h, http.MethodPost, "/v1/review", "mod-token", review)
	if rec.Code != http.StatusOK {
		t.Fatalf("moderator review: %d %s", rec.Code, rec.Body)
	}

	// Librarian may set publish (queued until the publisher lands).
	seed(t, svc, "wzzz999zzz999")
	review["decisions"].([]map[string]any)[0]["workId"] = "wzzz999zzz999"
	review["publish"] = true
	rec = doJSON(t, h, http.MethodPost, "/v1/review", "lib-token", review)
	if rec.Code != http.StatusOK {
		t.Fatalf("librarian review: %d %s", rec.Code, rec.Body)
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["approvedPending"].(float64) < 2 {
		t.Fatalf("resp = %v", resp)
	}

	// Audit: librarian only, month-validated.
	if rec := doJSON(t, h, http.MethodGet, "/v1/audit?month=2026-07", "mod-token", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("moderator audit: %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodGet, "/v1/audit?month=nope", "lib-token", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad month: %d", rec.Code)
	}
	month := page.Items[0].CreatedAt.UTC().Format("2006-01")
	rec = doJSON(t, h, http.MethodGet, "/v1/audit?month="+month, "lib-token", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit: %d", rec.Code)
	}
	var audit struct {
		Entries []suggest.AuditEntry `json:"entries"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &audit)
	if len(audit.Entries) != 2 {
		t.Fatalf("audit entries = %+v", audit.Entries)
	}
}

// TestMonthDefaultsToCurrentUTC covers tasks/234: on the month-keyed staff
// reports an absent month means "this month", while a malformed one still
// refuses. Absent and wrong are different answers, not the same 400.
func TestMonthDefaultsToCurrentUTC(t *testing.T) {
	h, _ := newModerationAPI(t)
	now := time.Now().UTC().Format("2006-01")

	for _, path := range []string{"/v1/stats", "/v1/audit"} {
		rec := doJSON(t, h, http.MethodGet, path, "lib-token", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s with no month = %d %s", path, rec.Code, rec.Body)
		}
		var body struct {
			Month string `json:"month"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Month != now {
			t.Errorf("GET %s defaulted to month %q, want the current UTC month %q", path, body.Month, now)
		}
		// Wrong is still wrong, and the message shows the shape.
		rec = doJSON(t, h, http.MethodGet, path+"?month=nope", "lib-token", nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s?month=nope = %d, want 400", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "month=2026-07") {
			t.Errorf("GET %s?month=nope error names no example: %s", path, rec.Body)
		}
		// An empty value reads as absent -- ?month= is what a form with a
		// cleared field sends, and it means the same thing as omitting it.
		if rec := doJSON(t, h, http.MethodGet, path+"?month=", "lib-token", nil); rec.Code != http.StatusOK {
			t.Errorf("GET %s?month= (empty) = %d, want the default", path, rec.Code)
		}
	}
}

func TestManualAndFolkGovernanceRoutes(t *testing.T) {
	h, svc := newModerationAPI(t)

	// Manual term: librarian only.
	manual := map[string]any{
		"action": "manual", "workId": "wabc123def456",
		"term": map[string]string{"scheme": "homosaurus", "id": transURI},
	}
	if rec := doJSON(t, h, http.MethodPost, "/v1/terms", "mod-token", manual); rec.Code != http.StatusForbidden {
		t.Fatalf("moderator manual term: %d", rec.Code)
	}
	if rec := doJSON(t, h, http.MethodPost, "/v1/terms", "lib-token", manual); rec.Code != http.StatusCreated {
		t.Fatalf("manual term: %d", rec.Code)
	}

	// Folk accept flows into folk autocomplete.
	_, err := svc.Submit(t.Context(), suggest.SubmitInput{
		WorkID: "wabc123def456", Term: vocab.TermRef{Scheme: vocab.FolkScheme, ID: "Cozy Fantasy"},
		Type: suggest.TypeAdd, SupporterHash: "h",
	})
	if err != nil {
		t.Fatal(err)
	}
	// PROPOSED: invisible.
	rec := doJSON(t, h, http.MethodGet, "/v1/terms?scheme=folk&q=cozy", "", nil)
	var out struct {
		Terms []vocab.TermRef `json:"terms"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Terms) != 0 {
		t.Fatalf("proposed term visible: %+v", out.Terms)
	}
	accept := map[string]any{"action": "acceptFolk", "folkTerm": "cozy fantasy"}
	if rec := doJSON(t, h, http.MethodPost, "/v1/terms", "lib-token", accept); rec.Code != http.StatusNoContent {
		t.Fatalf("accept folk: %d", rec.Code)
	}
	rec = doJSON(t, h, http.MethodGet, "/v1/terms?scheme=folk&q=cozy", "", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Terms) != 1 || out.Terms[0].ID != "cozy fantasy" {
		t.Fatalf("accepted terms = %+v", out.Terms)
	}
}
