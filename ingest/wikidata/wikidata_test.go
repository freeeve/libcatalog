package wikidata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"
	"github.com/freeeve/libcodex/rdf"
)

// binding builds one SPARQL JSON binding row.
func binding(isbn, qid, label, prop, valueQID, valueLabel string) map[string]any {
	b := map[string]any{
		"isbn":        map[string]any{"value": isbn},
		"author":      map[string]any{"value": "http://www.wikidata.org/entity/" + qid},
		"authorLabel": map[string]any{"value": label},
	}
	if prop != "" {
		b["prop"] = map[string]any{"value": "http://www.wikidata.org/prop/direct/" + prop}
		b["value"] = map[string]any{"value": "http://www.wikidata.org/entity/" + valueQID}
		b["valueLabel"] = map[string]any{"value": valueLabel}
	}
	return b
}

// stubSPARQL answers every query with the given bindings and records the
// query text.
type stubSPARQL struct {
	bindings []map[string]any
	queries  []string
}

func (s *stubSPARQL) Do(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	form, _ := url.ParseQuery(string(body))
	s.queries = append(s.queries, form.Get("query"))
	payload, _ := json.Marshal(map[string]any{
		"results": map[string]any{"bindings": s.bindings},
	})
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(string(payload))),
	}, nil
}

func summary(workID string, isbns ...string) ingest.WorkSummary {
	return ingest.WorkSummary{WorkID: workID, Title: "T " + workID, ISBNs: isbns}
}

// TestEnrichResolvesExplicitClaims: an ISBN-matched creator comes back with
// its explicitly-stated claims, provenance, and a deduped claim list; a work
// whose ISBN matched nothing is absent from the result entirely.
func TestEnrichResolvesExplicitClaims(t *testing.T) {
	stub := &stubSPARQL{bindings: []map[string]any{
		binding("9780062278241", "Q231663", "N.D. Stevenson", "P21", "Q48270", "non-binary"),
		binding("9780062278241", "Q231663", "N.D. Stevenson", "P21", "Q48270", "non-binary"), // dup row
		binding("9780062278241", "Q231663", "N.D. Stevenson", "P27", "Q30", "United States of America"),
	}}
	e := New(WithClient(stub), WithDelay(0))

	res, err := e.Enrich(context.Background(), []ingest.WorkSummary{
		summary("w1", "978-0-06-227824-1"),
		summary("w2", "9799999999990"), // unknown to the stub: must be absent
		summary("w3"),                  // no ISBN at all: must be absent
	})
	if err != nil {
		t.Fatalf("Enrich: %v", err)
	}
	if len(res) != 1 || res[0].WorkID != "w1" {
		t.Fatalf("results = %+v, want exactly w1", res)
	}
	if len(res[0].Creators) != 1 {
		t.Fatalf("creators = %+v, want one", res[0].Creators)
	}
	c := res[0].Creators[0]
	if c.QID != "Q231663" || c.Label != "N.D. Stevenson" {
		t.Errorf("creator = %+v", c)
	}
	if c.MatchedVia != "isbn:9780062278241" {
		t.Errorf("MatchedVia = %q (provenance must name the matching identifier)", c.MatchedVia)
	}
	if c.Retrieved == "" {
		t.Error("Retrieved date missing")
	}
	if len(c.Claims) != 2 {
		t.Fatalf("claims = %+v, want P21+P27 deduped", c.Claims)
	}
	if c.Claims[0].Property != "P21" || c.Claims[0].ValueQID != "Q48270" {
		t.Errorf("claim[0] = %+v", c.Claims[0])
	}

	// The query itself must resolve by identifier only: no label/name terms.
	q := stub.queries[0]
	if !strings.Contains(q, "9780062278241") || strings.Contains(q, "Stevenson") {
		t.Errorf("query must match by ISBN and never by name:\n%s", q)
	}
}

// TestEnrichMatchedButUnknown: a resolved author with NO stated claims still
// comes back (the audit counts matched-but-unknown honestly).
func TestEnrichMatchedButUnknown(t *testing.T) {
	stub := &stubSPARQL{bindings: []map[string]any{
		binding("9780062278241", "Q231663", "N.D. Stevenson", "", "", ""),
	}}
	e := New(WithClient(stub), WithDelay(0))
	res, err := e.Enrich(context.Background(), []ingest.WorkSummary{summary("w1", "9780062278241")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || len(res[0].Creators) != 1 {
		t.Fatalf("want the resolved creator: %+v", res)
	}
	if got := res[0].Creators[0]; len(got.Claims) != 0 {
		t.Errorf("claims = %+v, want none (matched but unknown)", got.Claims)
	}
}

// TestRunEnrichWritesCreatorGraph drives the whole seam: grain in a store,
// enrichment statements land in enrichment:wikidata with the work->entity
// link, the wdt: claims, and the provenance; a re-run is byte-stable.
func TestRunEnrichWritesCreatorGraph(t *testing.T) {
	dir := t.TempDir()
	bs := blob.NewDir(dir)
	ctx := context.Background()

	const workID = "wwd00000001a"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("coll")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"),
		rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/Work"), feed)
	inst := rdf.NewIRI("#" + workID + "Instance")
	ds.Add(work, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/hasInstance"), inst, feed)
	ident := rdf.NewIRI("#" + workID + "Isbn")
	ds.Add(inst, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/identifiedBy"), ident, feed)
	ds.Add(ident, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"),
		rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/Isbn"), feed)
	ds.Add(ident, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#value"),
		rdf.NewLiteral("9780062278241", "", ""), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(ctx, bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	stub := &stubSPARQL{bindings: []map[string]any{
		binding("9780062278241", "Q231663", "N.D. Stevenson", "P21", "Q48270", "non-binary"),
	}}
	e := New(WithClient(stub), WithDelay(0))
	n, err := ingest.RunEnrich(ctx, bs, "", e)
	if err != nil {
		t.Fatalf("RunEnrich: %v", err)
	}
	if n != 1 {
		t.Fatalf("enriched %d works, want 1", n)
	}

	grain, _, err := bs.Get(ctx, bibframe.GrainPath(workID))
	if err != nil {
		t.Fatal(err)
	}
	text := string(grain)
	for _, want := range []string{
		"<enrichment:wikidata>",
		bibframe.PredCreatorIdentity,
		"http://www.wikidata.org/entity/Q231663",
		"http://www.wikidata.org/prop/direct/P21",
		"http://www.wikidata.org/entity/Q48270",
		`"non-binary"`,
		`"isbn:9780062278241"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("grain missing %q:\n%s", want, text)
		}
	}

	// Idempotence: a second run replaces the graph byte-identically.
	if _, err := ingest.RunEnrich(ctx, bs, "", e); err != nil {
		t.Fatal(err)
	}
	again, _, err := bs.Get(ctx, bibframe.GrainPath(workID))
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != text {
		t.Error("re-run changed the grain (enrichment graph not idempotent)")
	}
}

// TestEnrichBatches: more ISBNs than one batch splits into several queries.
func TestEnrichBatches(t *testing.T) {
	stub := &stubSPARQL{}
	e := New(WithClient(stub), WithDelay(0))
	e.batch = 2
	var works []ingest.WorkSummary
	for i := 0; i < 5; i++ {
		works = append(works, summary(fmt.Sprintf("w%d", i), fmt.Sprintf("978000000000%d", i)))
	}
	if _, err := e.Enrich(context.Background(), works); err != nil {
		t.Fatal(err)
	}
	if len(stub.queries) != 3 {
		t.Errorf("queries = %d, want 3 (5 isbns / batch of 2)", len(stub.queries))
	}
}

// TestNormalizeISBN guards the identifier hygiene: hyphens/spaces strip, a
// final X survives, junk drops.
func TestNormalizeISBN(t *testing.T) {
	cases := map[string]string{
		"978-0-06-227824-1": "9780062278241",
		"0 06 227824 X":     "006227824X",
		"não-um-isbn":       "",
		"12345":             "",
	}
	for in, want := range cases {
		if got := normalizeISBN(in); got != want {
			t.Errorf("normalizeISBN(%q) = %q, want %q", in, got, want)
		}
	}
}

// flakySPARQL fails the first n requests with the given status, then delegates
// to the stub; requests whose body contains poison always fail.
type flakySPARQL struct {
	stubSPARQL
	failFirst int
	status    int
	poison    string
	attempts  int
}

func (f *flakySPARQL) Do(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	f.attempts++
	if (f.failFirst > 0 && f.attempts <= f.failFirst) ||
		(f.poison != "" && strings.Contains(string(body), f.poison)) {
		return &http.Response{StatusCode: f.status, Body: io.NopCloser(strings.NewReader("upstream request timeout"))}, nil
	}
	req.Body = io.NopCloser(strings.NewReader(string(body)))
	return f.stubSPARQL.Do(req)
}

// TestEnrichRetriesTransient504: WDQS weather (a 504 on the first attempts)
// retries with backoff and the run completes with no skips.
func TestEnrichRetriesTransient504(t *testing.T) {
	flaky := &flakySPARQL{
		stubSPARQL: stubSPARQL{bindings: []map[string]any{
			binding("9780062278241", "Q231663", "N.D. Stevenson", "P21", "Q48270", "non-binary"),
		}},
		failFirst: 2, status: http.StatusGatewayTimeout,
	}
	e := New(WithClient(flaky), WithDelay(0), WithRetryBase(0))
	res, err := e.Enrich(context.Background(), []ingest.WorkSummary{summary("w1", "9780062278241")})
	if err != nil || len(res) != 1 {
		t.Fatalf("Enrich = %v, %v; want the resolved work after retries", res, err)
	}
	if e.Skipped() != 0 {
		t.Errorf("skipped = %d, want 0 (the batch eventually succeeded)", e.Skipped())
	}
	if flaky.attempts != 3 {
		t.Errorf("attempts = %d, want 3 (two 504s then success)", flaky.attempts)
	}
}

// TestEnrichSkipsDeadBatchAndContinues: a batch that keeps 504ing past the
// retry budget is skipped -- its works stay untouched -- and the rest of the
// run proceeds and returns nil error.
func TestEnrichSkipsDeadBatchAndContinues(t *testing.T) {
	flaky := &flakySPARQL{
		stubSPARQL: stubSPARQL{bindings: []map[string]any{
			binding("9780062278241", "Q231663", "N.D. Stevenson", "P21", "Q48270", "non-binary"),
		}},
		poison: "9781111111111", status: http.StatusGatewayTimeout,
	}
	e := New(WithClient(flaky), WithDelay(0), WithRetryBase(0))
	e.batch = 1 // one ISBN per query so the poison isolates to its own batch
	res, err := e.Enrich(context.Background(), []ingest.WorkSummary{
		summary("wdead", "9781111111111"),
		summary("wok", "9780062278241"),
	})
	if err != nil {
		t.Fatalf("a partially-failed run must not error: %v", err)
	}
	if len(res) != 1 || res[0].WorkID != "wok" {
		t.Fatalf("results = %+v, want only the surviving batch's work", res)
	}
	if e.Skipped() != 1 {
		t.Errorf("skipped = %d, want 1", e.Skipped())
	}
}

// TestEnrichAllBatchesFailedErrors: when nothing succeeds the run errors --
// that shape is an outage or a misconfiguration, not weather.
func TestEnrichAllBatchesFailedErrors(t *testing.T) {
	flaky := &flakySPARQL{poison: "978", status: http.StatusGatewayTimeout}
	e := New(WithClient(flaky), WithDelay(0), WithRetryBase(0))
	if _, err := e.Enrich(context.Background(), []ingest.WorkSummary{summary("w1", "9780062278241")}); err == nil {
		t.Fatal("an all-batches-failed run should error")
	}
}

// TestEnrichBadRequestFailsFast: a 400 means the query itself is broken;
// retrying it is noise, so it fails after one attempt.
func TestEnrichBadRequestFailsFast(t *testing.T) {
	flaky := &flakySPARQL{poison: "978", status: http.StatusBadRequest}
	e := New(WithClient(flaky), WithDelay(0), WithRetryBase(0))
	if _, err := e.Enrich(context.Background(), []ingest.WorkSummary{summary("w1", "9780062278241")}); err == nil {
		t.Fatal("want error")
	}
	if flaky.attempts != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on a 400)", flaky.attempts)
	}
}

// TestQueryIsPortableSPARQL: the query must declare every prefix it uses and
// avoid WDQS-only extensions (the label SERVICE), so a spec-compliant mirror
// (QLever) parses it.
func TestQueryIsPortableSPARQL(t *testing.T) {
	stub := &stubSPARQL{}
	e := New(WithClient(stub), WithDelay(0))
	if _, err := e.Enrich(context.Background(), []ingest.WorkSummary{summary("w1", "9780062278241")}); err != nil {
		t.Fatal(err)
	}
	q := stub.queries[0]
	for _, want := range []string{
		"PREFIX wdt: <http://www.wikidata.org/prop/direct/>",
		"PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>",
	} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q:\n%s", want, q)
		}
	}
	if strings.Contains(q, "SERVICE wikibase:label") {
		t.Errorf("query uses the WDQS-only label service:\n%s", q)
	}
	// Every prefix the body uses must be declared.
	for _, used := range []string{"wdt:", "rdfs:"} {
		if strings.Count(q, used) < 2 { // at least the declaration + one use
			t.Errorf("prefix %s declared but unused, or used undeclared:\n%s", used, q)
		}
	}
}
