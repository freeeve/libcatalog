package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/freeeve/libcat/backend/suggest"
)

func reviewBody(workID string, approve bool, note string) map[string]any {
	return map[string]any{
		"decisions": []map[string]any{{
			"workId": workID,
			"term":   map[string]string{"scheme": "homosaurus", "id": transURI},
			"type":   "ADD", "approve": approve, "note": note,
		}},
		"publish": false,
	}
}

func postReview(t *testing.T, h http.Handler, token string, body map[string]any) map[string]any {
	t.Helper()
	rec := doJSON(t, h, http.MethodPost, "/v1/review", token, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v1/review: %d %s", rec.Code, rec.Body)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// "reviewed" counted the decisions submitted, not the ones applied,
// so a moderator whose decision lost a race was told it had been reviewed.
func TestReviewedCountsOnlyAppliedDecisions(t *testing.T) {
	h, svc := newModerationAPI(t)
	seed(t, svc, "wabc123def456")

	// A rejects it: a real transition.
	resp := postReview(t, h, "mod-token", reviewBody("wabc123def456", false, "A rejects"))
	if resp["reviewed"].(float64) != 1 {
		t.Fatalf("A's review: reviewed = %v, want 1", resp["reviewed"])
	}
	if _, ok := resp["staleDecisions"]; ok {
		t.Fatalf("A's review reported stale decisions: %v", resp)
	}

	// B approves the same suggestion from a queue page that predates A's apply.
	resp = postReview(t, h, "mod-token", reviewBody("wabc123def456", true, "B approves"))
	if resp["reviewed"].(float64) != 0 {
		t.Fatalf("B's stale approve: reviewed = %v, want 0", resp["reviewed"])
	}
	stale, ok := resp["staleDecisions"].([]any)
	if !ok || len(stale) != 1 {
		t.Fatalf("B's stale approve was not returned: %v", resp)
	}

	// The record still carries A's decision.
	items, err := svc.ForWork(t.Context(), "wabc123def456")
	if err != nil || len(items) != 1 {
		t.Fatalf("ForWork: %+v %v", items, err)
	}
	if items[0].Status != suggest.StatusRejected || items[0].ReviewNote != "A rejects" {
		t.Fatalf("B's stale approve changed the record: %+v", items[0])
	}
}

// The stale decisions are not called "skipped" in the response, because
// publish.Result already owns that key and maps.Copy would overwrite ours --
// on exactly the "Apply & publish" path where both halves report something.
func TestStaleDecisionsDoNotCollideWithThePublishSkippedCount(t *testing.T) {
	h, svc := newModerationAPI(t)
	seed(t, svc, "wabc123def456")

	// Resolve it once so the librarian's identical decision goes stale.
	postReview(t, h, "mod-token", reviewBody("wabc123def456", false, "first"))

	body := reviewBody("wabc123def456", true, "stale, and publishing")
	body["publish"] = true
	rec := doJSON(t, h, http.MethodPost, "/v1/review", "lib-token", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("librarian review+publish: %d %s", rec.Code, rec.Body)
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["reviewed"].(float64) != 0 {
		t.Fatalf("reviewed = %v, want 0: the decision was stale", resp["reviewed"])
	}
	stale, ok := resp["staleDecisions"].([]any)
	if !ok || len(stale) != 1 {
		t.Fatalf("the stale decision is missing from a publishing review: %v", resp)
	}
	// "skipped", if present, belongs to publish and means something else.
	if _, isPublishKey := resp["published"]; !isPublishKey {
		t.Fatalf("the publish half did not run, so this test proves nothing: %v", resp)
	}
}
