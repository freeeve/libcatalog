// GET /v1/audit/diversity -- the live coverage-first content audit
// over the work index, matching `lcat audit` semantics: subject URIs match by
// scheme, heading labels and tags by keyword, extras drive ?filter/?source.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
	"github.com/freeeve/libcodex/rdf"
)

// seedAuditWork writes a work grain with an optional controlled subject (uri +
// prefLabel), an optional uncontrolled tag (blank-node bf:subject), and optional
// extras.
func seedAuditWork(t *testing.T, bs blob.Store, workID, uri, prefLabel, tag string, extras map[string]string) {
	t.Helper()
	const (
		bfNS      = "http://id.loc.gov/ontologies/bibframe/"
		rdfType   = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
		prefLbl   = "http://www.w3.org/2004/02/skos/core#prefLabel"
		rdfsLabel = "http://www.w3.org/2000/01/rdf-schema#label"
	)
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("coll")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	ds.Add(work, rdf.NewIRI(rdfType), rdf.NewIRI(bfNS+"Work"), feed)
	titleNode := rdf.NewIRI("#" + workID + "Title")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), titleNode, feed)
	ds.Add(titleNode, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral("T "+workID, "", ""), feed)
	if uri != "" {
		subj := rdf.NewIRI(uri)
		ds.Add(work, rdf.NewIRI(bfNS+"subject"), subj, feed)
		if prefLabel != "" {
			ds.Add(subj, rdf.NewIRI(prefLbl), rdf.NewLiteral(prefLabel, "en", ""), feed)
		}
	}
	if tag != "" {
		topic := rdf.NewBlank("t1")
		ds.Add(work, rdf.NewIRI(bfNS+"subject"), topic, feed)
		ds.Add(topic, rdf.NewIRI(rdfsLabel), rdf.NewLiteral(tag, "", ""), feed)
	}
	for k, v := range extras {
		ds.Add(work, rdf.NewIRI(bibframe.ExtraPred+k), rdf.NewLiteral(v, "", ""), feed)
	}
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

type auditPage struct {
	Input        string `json:"input"`
	Scope        string `json:"scope"`
	TotalWorks   int    `json:"totalWorks"`
	CoveredWorks int    `json:"coveredWorks"`
	Categories   []struct {
		ID    string `json:"id"`
		Works int    `json:"works"`
	} `json:"categories"`
	Creators *struct {
		TotalWorks       int     `json:"totalWorks"`
		MatchedWorks     int     `json:"matchedWorks"`
		MatchRate        float64 `json:"matchRate"`
		ResolvedCreators int     `json:"resolvedCreators"`
		Properties       []struct {
			Property string `json:"property"`
			Known    int    `json:"known"`
			Unknown  int    `json:"unknown"`
			Values   []struct {
				Label    string `json:"label"`
				Creators int    `json:"creators"`
			} `json:"values"`
		} `json:"properties"`
	} `json:"creators"`
}

func getAudit(t *testing.T, h http.Handler, query string) auditPage {
	t.Helper()
	url := "/v1/audit/diversity"
	if query != "" {
		url += "?" + query
	}
	rec := request(t, h, http.MethodGet, url, "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (%s)", url, rec.Code, rec.Body.String())
	}
	var page auditPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

func auditCat(p auditPage, id string) int {
	for _, c := range p.Categories {
		if c.ID == id {
			return c.Works
		}
	}
	return -1
}

func TestAuditDiversity(t *testing.T) {
	h, bs := newRecordsAPI(t)
	// w1: Homosaurus URI with a keyword-less label -> lgbtqia via SCHEME.
	seedAuditWork(t, bs, "waudit00001a", "https://homosaurus.org/v5/homoit0000506", "Chosen family", "",
		map[string]string{"inQll": "true"})
	// w2: FAST URI whose HEADING label matches a keyword (plural-tolerant).
	seedAuditWork(t, bs, "waudit00001b", "http://id.worldcat.org/fast/995592", "Lesbians", "", nil)
	// w3: uncontrolled TAG only.
	seedAuditWork(t, bs, "waudit00001c", "", "", "Immigrants", nil)
	// w4: no aboutness signal at all -- dilutes coverage.
	seedAuditWork(t, bs, "waudit00001d", "", "", "", nil)

	p := getAudit(t, h, "")
	if p.TotalWorks != 4 || p.CoveredWorks != 3 {
		t.Errorf("totals = %d/%d, want 4 total / 3 covered", p.TotalWorks, p.CoveredWorks)
	}
	if got := auditCat(p, "lgbtqia"); got != 2 {
		t.Errorf("lgbtqia = %d, want 2 (scheme + heading-keyword paths)", got)
	}
	if got := auditCat(p, "immigrant-diaspora"); got != 1 {
		t.Errorf("immigrant-diaspora = %d, want 1 (tag path)", got)
	}
	if p.Input == "" {
		t.Error("response should name its input")
	}
	// No cached creator claims in this corpus: the creators block is absent
	// (opt-in source not run), which must read differently from a 0% match.
	if p.Creators != nil {
		t.Errorf("creators block should be absent with no cached claims: %+v", p.Creators)
	}

	// ?filter scopes by extras and is named in the response.
	p = getAudit(t, h, "filter=inQll%3Dtrue")
	if p.TotalWorks != 1 || auditCat(p, "lgbtqia") != 1 {
		t.Errorf("filtered = %d works / lgbtqia %d, want 1/1", p.TotalWorks, auditCat(p, "lgbtqia"))
	}
	if p.Scope != "inQll=true" {
		t.Errorf("scope = %q, want inQll=true", p.Scope)
	}

	// A malformed filter is a 400, not a silent full-corpus report.
	rec := request(t, h, http.MethodGet, "/v1/audit/diversity?filter=nokey", "lib-token", "", nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad filter = %d, want 400", rec.Code)
	}
}

// seedCreatorClaims adds an enrichment:wikidata graph to a work's grain: the
// creator-identity link plus one explicit P21 claim, the shape the wikidata
// enricher writes.
func seedCreatorClaims(t *testing.T, bs blob.Store, workID, qid, valueQID, valueLabel string) {
	t.Helper()
	path := bibframe.GrainPath(workID)
	grain, etag, err := bs.Get(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	const (
		wd  = "http://www.wikidata.org/entity/"
		wdt = "http://www.wikidata.org/prop/direct/"
		lbl = "http://www.w3.org/2000/01/rdf-schema#label"
	)
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		t.Fatal(err)
	}
	g := rdf.NewIRI("enrichment:wikidata")
	ent := rdf.NewIRI(wd + qid)
	ds.Add(rdf.NewIRI(bibframe.WorkIRI(workID)), rdf.NewIRI(bibframe.PredCreatorIdentity), ent, g)
	ds.Add(ent, rdf.NewIRI(wdt+"P21"), rdf.NewIRI(wd+valueQID), g)
	ds.Add(rdf.NewIRI(wd+valueQID), rdf.NewIRI(lbl), rdf.NewLiteral(valueLabel, "en", ""), g)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), path, nq, blob.PutOptions{IfMatch: etag}); err != nil {
		t.Fatal(err)
	}
}

// TestAuditDiversityCreators: cached wikidata claims aggregate into the
// creators block -- match rate against the same scope, distinct-creator value
// distributions with unknowns, no names anywhere. Absent entirely when the
// corpus carries no creator data.
func TestAuditDiversityCreators(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedAuditWork(t, bs, "waudit00002a", "", "", "Lesbians", nil)
	seedAuditWork(t, bs, "waudit00002b", "", "", "Gay men", nil)
	seedAuditWork(t, bs, "waudit00002c", "", "", "", nil)
	// Two works share one resolved creator; a third work stays unmatched.
	// Seeded before the first request: a direct blob write bypasses the
	// index's update hooks, so it must be present at the initial scan.
	seedCreatorClaims(t, bs, "waudit00002a", "Q42", "Q6581097", "male")
	seedCreatorClaims(t, bs, "waudit00002b", "Q42", "Q6581097", "male")

	p := getAudit(t, h, "")
	ca := p.Creators
	if ca == nil {
		t.Fatal("creators block missing")
	}
	if ca.TotalWorks != 3 || ca.MatchedWorks != 2 {
		t.Errorf("matched = %d/%d, want 2/3", ca.MatchedWorks, ca.TotalWorks)
	}
	if ca.ResolvedCreators != 1 {
		t.Errorf("resolvedCreators = %d, want 1 (Q42 deduped across works)", ca.ResolvedCreators)
	}
	if len(ca.Properties) != 4 {
		t.Fatalf("properties = %d, want the 4 audited", len(ca.Properties))
	}
	p21 := ca.Properties[0]
	if p21.Property != "P21" || p21.Known != 1 || p21.Unknown != 0 {
		t.Errorf("P21 = %+v, want known 1 / unknown 0", p21)
	}
	if len(p21.Values) != 1 || p21.Values[0].Label != "male" || p21.Values[0].Creators != 1 {
		t.Errorf("P21 values = %+v", p21.Values)
	}
	// The un-stated properties report the resolved creator as unknown.
	if p27 := ca.Properties[1]; p27.Known != 0 || p27.Unknown != 1 {
		t.Errorf("P27 = %+v, want known 0 / unknown 1", p27)
	}
}

// TestAuditCache: same generation + normalized filter key serves the stored
// response; a generation change or cap overflow drops entries; term order does
// not fork the cache.
func TestAuditCache(t *testing.T) {
	c := &auditCache{}
	r1 := auditResponse{Scope: "one"}
	c.put(7, "k", r1)
	if got, ok := c.get(7, "k"); !ok || got.Scope != "one" {
		t.Fatalf("get after put = %+v, %v", got, ok)
	}
	if _, ok := c.get(8, "k"); ok {
		t.Fatal("a new generation must miss")
	}
	c.put(8, "k", auditResponse{Scope: "two"})
	if _, ok := c.get(7, "k"); ok {
		t.Fatal("the old generation must be gone after a newer put")
	}
	// Cap overflow clears wholesale rather than evicting piecemeal.
	for i := 0; i < auditCacheCap+1; i++ {
		c.put(8, fmt.Sprintf("k%d", i), auditResponse{})
	}
	if len(c.entries) > auditCacheCap {
		t.Fatalf("entries = %d, want <= cap", len(c.entries))
	}

	// Key normalization: filter order must not fork entries.
	a := auditFilterSet{{"a", "1"}, {"b", "2"}}
	b := auditFilterSet{{"b", "2"}, {"a", "1"}}
	if a.cacheKey() != b.cacheKey() {
		t.Errorf("cacheKey order-sensitive: %q vs %q", a.cacheKey(), b.cacheKey())
	}
}
