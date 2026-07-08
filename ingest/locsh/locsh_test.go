package locsh

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

// fixtures maps suggest2 q values to recorded-shape responses (no live
// network in tests).
var fixtures = map[string]suggestResponse{
	"science fiction": {Hits: []struct {
		URI          string `json:"uri"`
		ALabel       string `json:"aLabel"`
		SuggestLabel string `json:"suggestLabel"`
	}{
		{URI: "http://id.loc.gov/authorities/subjects/sh85118553", ALabel: "Science fiction"},
	}},
	"necromancy": {Hits: []struct {
		URI          string `json:"uri"`
		ALabel       string `json:"aLabel"`
		SuggestLabel string `json:"suggestLabel"`
	}{
		{URI: "http://id.loc.gov/authorities/subjects/sh85090542", ALabel: "Necromancy in literature"},
	}},
	"zebra hats": {},
}

func newFixtureServer(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		q := r.URL.Query().Get("q")
		resp, ok := fixtures[q]
		if !ok {
			t.Errorf("unexpected lookup %q", q)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestEnrichReconcilesTags(t *testing.T) {
	srv, calls := newFixtureServer(t)
	e := &Enricher{BaseURL: srv.URL, Client: srv.Client()}
	works := []ingest.WorkSummary{
		{WorkID: "w1", Tags: []string{"Science Fiction.", "necromancy"}},
		{WorkID: "w2", Tags: []string{"Science Fiction", "Zebra Hats"}},
		{WorkID: "w3"},
	}
	results, err := e.Enrich(t.Context(), works)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	// w1: exact match (MARC trailing period normalized away), confidence 1;
	// the prefix-matched "Necromancy in literature" (0.6) falls below the
	// default 0.9 floor.
	if results[0].WorkID != "w1" || len(results[0].Subjects) != 1 ||
		results[0].Subjects[0].URI != "http://id.loc.gov/authorities/subjects/sh85118553" {
		t.Fatalf("w1 = %+v", results[0])
	}
	if results[0].Subjects[0].Labels["en"] != "Science fiction" || results[0].Confidence != 1 {
		t.Fatalf("w1 subject = %+v", results[0])
	}
	// w2: same subject; the no-hit tag contributes nothing.
	if results[1].WorkID != "w2" || len(results[1].Subjects) != 1 {
		t.Fatalf("w2 = %+v", results[1])
	}
	// The shared tag was looked up once, not per work.
	if *calls != 3 {
		t.Fatalf("lookups = %d, want 3 (cache miss per distinct tag)", *calls)
	}

	// Lowering the floor admits the prefix match.
	e2 := &Enricher{BaseURL: srv.URL, Client: srv.Client(), MinConfidence: 0.5}
	results, err = e2.Enrich(t.Context(), works[:1])
	if err != nil || len(results) != 1 || len(results[0].Subjects) != 2 {
		t.Fatalf("low floor = %+v, %v", results, err)
	}
	if results[0].Confidence != 0.6 {
		t.Fatalf("confidence = %v, want the weakest member 0.6", results[0].Confidence)
	}
}

func TestEnrichUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	e := &Enricher{BaseURL: srv.URL, Client: srv.Client()}
	if _, err := e.Enrich(t.Context(), []ingest.WorkSummary{{WorkID: "w1", Tags: []string{"x y"}}}); err == nil {
		t.Fatal("upstream failure swallowed")
	}
}
