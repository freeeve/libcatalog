// The operator crosswalk-configuration surface: GET/PUT/DELETE the persisted
// override (the audit reflects it immediately), POST preview runs a candidate
// crosswalk without persisting, and GET /v1/audit/terms serves the facet
// builder's term histogram.
package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

type crosswalkPage struct {
	Seed     []struct{ ID string } `json:"seed"`
	Override []struct {
		ID       string   `json:"id"`
		Label    string   `json:"label"`
		Keywords []string `json:"keywords"`
	} `json:"override"`
	TOML      string                       `json:"toml"`
	Effective []struct{ ID, Label string } `json:"effective"`
	Broken    string                       `json:"broken"`
}

func getCrosswalk(t *testing.T, h http.Handler) crosswalkPage {
	t.Helper()
	rec := request(t, h, http.MethodGet, "/v1/audit/diversity/crosswalk", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET crosswalk = %d (%s)", rec.Code, rec.Body)
	}
	var page crosswalkPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func TestAuditCrosswalkCRUD(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedAuditWork(t, bs, "waudit0cw01a", "", "", "veterans", nil)

	// Before any override: seed-only view, and the audit knows no "veterans".
	page := getCrosswalk(t, h)
	if len(page.Seed) == 0 || len(page.Override) != 0 || page.TOML != "" {
		t.Fatalf("pre-override view = %+v, want seed only", page)
	}
	if len(page.Effective) != len(page.Seed) {
		t.Errorf("effective (%d) != seed (%d) with no override", len(page.Effective), len(page.Seed))
	}
	if got := auditCat(getAudit(t, h, ""), "veterans"); got != -1 {
		t.Fatalf("veterans already in the seed audit: %d", got)
	}

	// PUT a structured override; the audit reflects it immediately (the
	// override hash keys the memoized report -- same corpus generation).
	rec := request(t, h, http.MethodPut, "/v1/audit/diversity/crosswalk", "lib-token", "", map[string]any{
		"categories": []map[string]any{{"id": "veterans", "label": "Veterans", "keywords": []string{"veterans"}}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT crosswalk = %d (%s)", rec.Code, rec.Body)
	}
	page = getCrosswalk(t, h)
	if len(page.Override) != 1 || page.Override[0].ID != "veterans" {
		t.Fatalf("override after PUT = %+v", page.Override)
	}
	if !strings.Contains(page.TOML, "[[category]]") || !strings.Contains(page.TOML, "veterans") {
		t.Errorf("stored TOML should be the CLI-portable dialect, got:\n%s", page.TOML)
	}
	if last := page.Effective[len(page.Effective)-1]; last.ID != "veterans" {
		t.Errorf("effective should append the new category, last = %+v", last)
	}
	if got := auditCat(getAudit(t, h, ""), "veterans"); got != 1 {
		t.Errorf("audit veterans = %d after override, want 1", got)
	}

	// PUT raw TOML (the paste-your---crosswalk-file path).
	rec = request(t, h, http.MethodPut, "/v1/audit/diversity/crosswalk", "lib-token", "", map[string]any{
		"toml": "[[category]]\nid = \"veterans\"\nlabel = \"Veterans & military families\"\nkeywords = [\"veterans\", \"military families\"]\n",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT toml = %d (%s)", rec.Code, rec.Body)
	}
	if page = getCrosswalk(t, h); len(page.Override) != 1 || len(page.Override[0].Keywords) != 2 {
		t.Fatalf("override after TOML PUT = %+v", page.Override)
	}

	// Invalid documents are refused: no id, both fields, neither field.
	for name, body := range map[string]map[string]any{
		"missing id":  {"categories": []map[string]any{{"label": "No id"}}},
		"both fields": {"categories": []map[string]any{{"id": "x"}}, "toml": "[[category]]\nid=\"x\"\n"},
		"neither":     {},
		"bad toml":    {"toml": "[[category]\nid ="},
	} {
		if rec := request(t, h, http.MethodPut, "/v1/audit/diversity/crosswalk", "lib-token", "", body); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", name, rec.Code)
		}
	}

	// DELETE returns to the seed, audit included.
	if rec := request(t, h, http.MethodDelete, "/v1/audit/diversity/crosswalk", "lib-token", "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d", rec.Code)
	}
	if page = getCrosswalk(t, h); len(page.Override) != 0 || page.TOML != "" {
		t.Fatalf("view after DELETE = %+v, want seed only", page)
	}
	if got := auditCat(getAudit(t, h, ""), "veterans"); got != -1 {
		t.Errorf("veterans survived the DELETE in the audit: %d", got)
	}
	// Deleting again (nothing persisted) stays a 204, not an error.
	if rec := request(t, h, http.MethodDelete, "/v1/audit/diversity/crosswalk", "lib-token", "", nil); rec.Code != http.StatusNoContent {
		t.Errorf("second DELETE = %d, want 204", rec.Code)
	}
}

func TestAuditCrosswalkPreviewDoesNotPersist(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedAuditWork(t, bs, "waudit0cw02a", "", "", "veterans", nil)
	seedAuditWork(t, bs, "waudit0cw02b", "http://id.worldcat.org/fast/995592", "Lesbians", "", nil)

	rec := request(t, h, http.MethodPost, "/v1/audit/diversity/preview", "lib-token", "", map[string]any{
		"categories": []map[string]any{{"id": "veterans", "label": "Veterans", "keywords": []string{"veterans"}}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d (%s)", rec.Code, rec.Body)
	}
	var page auditPage
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if got := auditCat(page, "veterans"); got != 1 {
		t.Errorf("preview veterans = %d, want 1", got)
	}
	// Seed categories still audit in the preview (merge, not replace).
	if got := auditCat(page, "lgbtqia"); got != 1 {
		t.Errorf("preview lgbtqia = %d, want 1", got)
	}

	// Nothing persisted: the stored view and the live audit are untouched.
	if page := getCrosswalk(t, h); len(page.Override) != 0 {
		t.Errorf("preview persisted an override: %+v", page.Override)
	}
	if got := auditCat(getAudit(t, h, ""), "veterans"); got != -1 {
		t.Errorf("preview leaked into the live audit: %d", got)
	}

	// A malformed candidate is a 400.
	if rec := request(t, h, http.MethodPost, "/v1/audit/diversity/preview", "lib-token", "", map[string]any{
		"toml": "[[category]\n",
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed preview = %d, want 400", rec.Code)
	}
}

func TestAuditTermsHistogram(t *testing.T) {
	h, bs := newRecordsAPI(t)
	const fast = "http://id.worldcat.org/fast/995592"
	seedAuditWork(t, bs, "waudit0cw03a", fast, "Lesbians", "", map[string]string{"inQll": "true"})
	seedAuditWork(t, bs, "waudit0cw03b", fast, "Lesbians", "", nil)
	seedAuditWork(t, bs, "waudit0cw03c", "", "", "zines", nil)

	rec := request(t, h, http.MethodGet, "/v1/audit/terms", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET terms = %d (%s)", rec.Code, rec.Body)
	}
	var page struct {
		TotalWorks int `json:"totalWorks"`
		URIs       []struct {
			URI    string `json:"uri"`
			Scheme string `json:"scheme"`
			Works  int    `json:"works"`
		} `json:"uris"`
		Headings []struct {
			Label string `json:"label"`
			Works int    `json:"works"`
		} `json:"headings"`
		Tags []struct {
			Label string `json:"label"`
			Works int    `json:"works"`
		} `json:"tags"`
		URITotal int `json:"uriTotal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.TotalWorks != 3 {
		t.Errorf("totalWorks = %d, want 3", page.TotalWorks)
	}
	found := false
	for _, u := range page.URIs {
		if u.URI == fast {
			found = true
			if u.Works != 2 || u.Scheme != "fast" {
				t.Errorf("fast term = %+v, want works 2 scheme fast", u)
			}
		}
	}
	if !found || page.URITotal < 1 {
		t.Fatalf("uris = %+v (total %d), want the seeded FAST term", page.URIs, page.URITotal)
	}
	if len(page.Headings) == 0 || page.Headings[0].Label != "Lesbians" || page.Headings[0].Works != 2 {
		t.Errorf("headings = %+v, want Lesbians x2 first", page.Headings)
	}
	if len(page.Tags) != 1 || page.Tags[0].Label != "zines" {
		t.Errorf("tags = %+v, want zines", page.Tags)
	}

	// Filters scope the histogram like the audit.
	rec = request(t, h, http.MethodGet, "/v1/audit/terms?filter=inQll=true", "lib-token", "", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if page.TotalWorks != 1 || len(page.URIs) != 1 || page.URIs[0].Works != 1 {
		t.Errorf("filtered histogram = %+v (total %d), want the one inQll work", page.URIs, page.TotalWorks)
	}

	// Limit is validated.
	if rec := request(t, h, http.MethodGet, "/v1/audit/terms?limit=0", "lib-token", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("limit=0 = %d, want 400", rec.Code)
	}
}
