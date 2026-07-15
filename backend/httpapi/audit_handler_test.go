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

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
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
	Input          string   `json:"input"`
	Scope          string   `json:"scope"`
	TotalWorks     int      `json:"totalWorks"`
	CoveredWorks   int      `json:"coveredWorks"`
	LabelLanguages []string `json:"labelLanguages"`
	Categories     []struct {
		ID             string         `json:"id"`
		Works          int            `json:"works"`
		LabelLangWorks map[string]int `json:"labelLangWorks"`
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
	Simulation *struct {
		Filter    string `json:"filter"`
		Applied   int    `json:"applied"`
		Works     int    `json:"works"`
		Projected struct {
			TotalWorks   int `json:"totalWorks"`
			CoveredWorks int `json:"coveredWorks"`
			Categories   []struct {
				ID    string `json:"id"`
				Works int    `json:"works"`
			} `json:"categories"`
		} `json:"projected"`
	} `json:"simulation"`
}

// projCat reads a category's work count from a simulation's projected report.
func projCat(p auditPage, id string) int {
	if p.Simulation == nil {
		return -1
	}
	for _, c := range p.Simulation.Projected.Categories {
		if c.ID == id {
			return c.Works
		}
	}
	return -1
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

// homosaurusVocab loads a three-term Homosaurus index into a fresh blob: an
// en+es concept, an en+fr concept, and an English-only one, so the audit's
// per-language subject-label columns have distinct languages to tally.
func homosaurusVocab(t *testing.T) *vocab.Index {
	t.Helper()
	const nq = `<https://homosaurus.org/v4/homoit0001235> <http://www.w3.org/2004/02/skos/core#prefLabel> "Transgender people"@en <authority:homosaurus> .
<https://homosaurus.org/v4/homoit0001235> <http://www.w3.org/2004/02/skos/core#prefLabel> "Personas transgénero"@es <authority:homosaurus> .
<https://homosaurus.org/v4/homoit0000900> <http://www.w3.org/2004/02/skos/core#prefLabel> "Genderqueer identity"@en <authority:homosaurus> .
<https://homosaurus.org/v4/homoit0000901> <http://www.w3.org/2004/02/skos/core#prefLabel> "Two-spirit people"@en <authority:homosaurus> .
<https://homosaurus.org/v4/homoit0000901> <http://www.w3.org/2004/02/skos/core#altLabel> "Personnes bispirituelles"@fr <authority:homosaurus> .
`
	bs := blob.NewMem()
	if _, err := bs.Put(t.Context(), "auth/homosaurus.nq", []byte(nq), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := vocab.Load(t.Context(), bs, "auth/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// TestAuditDiversityLanguageCoverage checks the per-language subject-label
// columns: works subjected with Homosaurus terms carrying es / fr labels count
// under those language keys, the baseline en counts every controlled subject,
// and the response echoes the configured language column set.
func TestAuditDiversityLanguageCoverage(t *testing.T) {
	bs := blob.NewMem()
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: store.NewMem(), Vocab: homosaurusVocab(t), AuditLangs: []string{"en", "es", "fr"}, Verifier: verifier})

	// wA: en+es Homosaurus subject -> lgbtqia via scheme.
	seedAuditWork(t, bs, "wauditlang0a", "https://homosaurus.org/v4/homoit0001235", "Transgender people", "", nil)
	// wB: English-only Homosaurus subject.
	seedAuditWork(t, bs, "wauditlang0b", "https://homosaurus.org/v4/homoit0000900", "Genderqueer identity", "", nil)
	// wC: en+fr Homosaurus subject.
	seedAuditWork(t, bs, "wauditlang0c", "https://homosaurus.org/v4/homoit0000901", "Two-spirit people", "", nil)

	p := getAudit(t, h, "")
	if got := p.LabelLanguages; len(got) != 3 || got[0] != "en" || got[1] != "es" || got[2] != "fr" {
		t.Errorf("labelLanguages = %v, want [en es fr]", got)
	}
	var lg map[string]int
	var works int
	var found bool
	for _, c := range p.Categories {
		if c.ID == "lgbtqia" {
			lg, works, found = c.LabelLangWorks, c.Works, true
		}
	}
	if !found {
		t.Fatal("lgbtqia category missing from report")
	}
	if works != 3 {
		t.Fatalf("lgbtqia works = %d, want 3", works)
	}
	if lg["en"] != 3 {
		t.Errorf("lgbtqia en = %d, want 3 (every controlled subject has an en label)", lg["en"])
	}
	if lg["es"] != 1 {
		t.Errorf("lgbtqia es = %d, want 1 (the en+es term)", lg["es"])
	}
	if lg["fr"] != 1 {
		t.Errorf("lgbtqia fr = %d, want 1 (the en+fr altLabel term)", lg["fr"])
	}
}

// seedAuditWorkLangs writes a work grain carrying bf:language IRIs (LoC
// vocabulary, three-letter codes) plus optional extras -- the resource language
// the audit's resource-language dimension counts at the Work level.
func seedAuditWorkLangs(t *testing.T, bs blob.Store, workID string, langs []string, extras map[string]string) {
	t.Helper()
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	const rdfType = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("coll")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	ds.Add(work, rdf.NewIRI(rdfType), rdf.NewIRI(bfNS+"Work"), feed)
	titleNode := rdf.NewIRI("#" + workID + "Title")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), titleNode, feed)
	ds.Add(titleNode, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral("T "+workID, "", ""), feed)
	for _, code := range langs {
		ds.Add(work, rdf.NewIRI(bfNS+"language"), rdf.NewIRI("http://id.loc.gov/vocabulary/languages/"+code), feed)
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

// resLangs pulls the resource-language block out of an audit response.
type resLangPage struct {
	ResourceLanguages *struct {
		TotalWorks   int `json:"totalWorks"`
		WithLanguage int `json:"withLanguage"`
		Multilingual int `json:"multilingual"`
		Languages    []struct {
			Code  string `json:"code"`
			Works int    `json:"works"`
		} `json:"languages"`
	} `json:"resourceLanguages"`
}

// TestAuditDiversityResourceLanguages checks the Work-level resource-language
// distribution: single-language works count under their language, multi-language
// works count once as Multilingual (never under each language, so an English
// title with a Spanish edition available does not inflate Spanish), buckets sum
// to WithLanguage, the collection scope is honoured, and the block is absent when
// no scoped work declares a language.
func TestAuditDiversityResourceLanguages(t *testing.T) {
	bs := blob.NewMem()
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier})

	inQll := map[string]string{"inQll": "true"}
	seedAuditWorkLangs(t, bs, "wreslang001", []string{"spa"}, inQll)
	seedAuditWorkLangs(t, bs, "wreslang002", []string{"eng"}, inQll)
	seedAuditWorkLangs(t, bs, "wreslang003", []string{"eng"}, inQll)
	seedAuditWorkLangs(t, bs, "wreslang004", nil, inQll)                    // in scope, no language
	seedAuditWorkLangs(t, bs, "wreslang005", []string{"fre"}, nil)          // out of the QLL scope
	seedAuditWorkLangs(t, bs, "wreslang006", []string{"eng", "spa"}, inQll) // multi-language availability

	// Full corpus: every work counted, fre included; the eng+spa work is
	// Multilingual, NOT a Spanish work.
	full := getResLangs(t, h, "")
	if full.ResourceLanguages == nil {
		t.Fatal("resource-language block missing on the full corpus")
	}
	rl := full.ResourceLanguages
	if rl.TotalWorks != 6 || rl.WithLanguage != 5 {
		t.Errorf("full totals = %d/%d, want 6 total / 5 with language", rl.TotalWorks, rl.WithLanguage)
	}
	if rl.Multilingual != 1 {
		t.Errorf("full multilingual = %d, want 1 (006 eng+spa)", rl.Multilingual)
	}
	if got := resLangCount(full, "eng"); got != 2 {
		t.Errorf("full eng = %d, want 2 (002, 003 -- not the multilingual 006)", got)
	}
	if got := resLangCount(full, "spa"); got != 1 {
		t.Errorf("full spa = %d, want 1 (001 only -- 006 is multilingual, not inflated into spa)", got)
	}
	if got := resLangCount(full, "fre"); got != 1 {
		t.Errorf("full fre = %d, want 1 (005)", got)
	}
	// Single-language buckets plus Multilingual reconcile to WithLanguage.
	sum := rl.Multilingual
	for _, l := range rl.Languages {
		sum += l.Works
	}
	if sum != rl.WithLanguage {
		t.Errorf("languages+multilingual = %d, want %d (WithLanguage)", sum, rl.WithLanguage)
	}
	// Most works first: eng leads.
	if rl.Languages[0].Code != "eng" {
		t.Errorf("languages should be ranked by works, got %+v", rl.Languages)
	}

	// Scoped to the QLL collection: fre (out of scope) drops out.
	scoped := getResLangs(t, h, "filter=inQll%3Dtrue")
	if scoped.ResourceLanguages == nil {
		t.Fatal("resource-language block missing on the scoped corpus")
	}
	if scoped.ResourceLanguages.TotalWorks != 5 || scoped.ResourceLanguages.WithLanguage != 4 {
		t.Errorf("scoped totals = %d/%d, want 5 total / 4 with language", scoped.ResourceLanguages.TotalWorks, scoped.ResourceLanguages.WithLanguage)
	}
	if got := resLangCount(scoped, "fre"); got != 0 {
		t.Errorf("scoped fre = %d, want 0 (005 is outside inQll)", got)
	}
	if got := resLangCount(scoped, "eng"); got != 2 {
		t.Errorf("scoped eng = %d, want 2", got)
	}
	if scoped.ResourceLanguages.Multilingual != 1 {
		t.Errorf("scoped multilingual = %d, want 1", scoped.ResourceLanguages.Multilingual)
	}

	// A corpus with no language data at all omits the block.
	bs2 := blob.NewMem()
	h2 := New(Deps{Blob: bs2, DB: store.NewMem(), Verifier: verifier})
	seedAuditWorkLangs(t, bs2, "wnolang0001", nil, inQll)
	none := getResLangs(t, h2, "")
	if none.ResourceLanguages != nil {
		t.Errorf("resource-language block should be absent with no language data: %+v", none.ResourceLanguages)
	}
}

// TestAuditResourceLanguagesMultilingualOnly checks the block still appears when
// every scoped work with a language is multilingual (no single-language work),
// so a Multilingual-only corpus is not mistaken for no language data.
func TestAuditResourceLanguagesMultilingualOnly(t *testing.T) {
	bs := blob.NewMem()
	verifier := staffVerifier{"lib-token": {Email: "lib@example.org", Roles: []auth.Role{auth.RoleLibrarian}}}
	h := New(Deps{Blob: bs, DB: store.NewMem(), Verifier: verifier})
	seedAuditWorkLangs(t, bs, "wmlonly0001", []string{"eng", "spa"}, nil)

	p := getResLangs(t, h, "")
	if p.ResourceLanguages == nil {
		t.Fatal("block should be present with a multilingual work")
	}
	if p.ResourceLanguages.WithLanguage != 1 || p.ResourceLanguages.Multilingual != 1 {
		t.Errorf("withLanguage/multilingual = %d/%d, want 1/1", p.ResourceLanguages.WithLanguage, p.ResourceLanguages.Multilingual)
	}
	if len(p.ResourceLanguages.Languages) != 0 {
		t.Errorf("no single-language works, want empty languages, got %+v", p.ResourceLanguages.Languages)
	}
}

// weightPage decodes the weight fields of an audit response.
type weightPage struct {
	TotalWeight int `json:"totalWeight"`
	Categories  []struct {
		ID     string `json:"id"`
		Works  int    `json:"works"`
		Weight int    `json:"weight"`
	} `json:"categories"`
}

// TestAuditDiversityHeldQuantity checks the copies-held weighting: the audit
// sums each work's ownedCopies extra into a per-category Weight and a corpus
// TotalWeight, so a category can be read by collection depth, not just title
// count. A missing extra weighs 0.
func TestAuditDiversityHeldQuantity(t *testing.T) {
	h, bs := newRecordsAPI(t)
	// Two LGBTQIA+ works via Homosaurus scheme, one deeply held, one single copy.
	seedAuditWork(t, bs, "wheld00001a", "https://homosaurus.org/v5/homoit0000506", "Chosen family", "",
		map[string]string{"ownedCopies": "12"})
	seedAuditWork(t, bs, "wheld00001b", "https://homosaurus.org/v5/homoit0000508", "Gender identity", "",
		map[string]string{"ownedCopies": "1"})
	// A work with no ownedCopies extra weighs 0 but still counts as a title.
	seedAuditWork(t, bs, "wheld00001c", "https://homosaurus.org/v5/homoit0000900", "Genderqueer", "", nil)

	rec := request(t, h, http.MethodGet, "/v1/audit/diversity", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit = %d (%s)", rec.Code, rec.Body.String())
	}
	var p weightPage
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.TotalWeight != 13 {
		t.Errorf("totalWeight = %d, want 13 (12+1+0)", p.TotalWeight)
	}
	var lg struct{ works, weight int }
	for _, c := range p.Categories {
		if c.ID == "lgbtqia" {
			lg.works, lg.weight = c.Works, c.Weight
		}
	}
	if lg.works != 3 {
		t.Fatalf("lgbtqia works = %d, want 3", lg.works)
	}
	if lg.weight != 13 {
		t.Errorf("lgbtqia weight = %d, want 13 (copies held, not the 3 titles)", lg.weight)
	}
}

// getResLangs fetches the audit and decodes only its resource-language block.
func getResLangs(t *testing.T, h http.Handler, query string) resLangPage {
	t.Helper()
	url := "/v1/audit/diversity"
	if query != "" {
		url += "?" + query
	}
	rec := request(t, h, http.MethodGet, url, "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (%s)", url, rec.Code, rec.Body.String())
	}
	var page resLangPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}

// resLangCount reads one language's work count from a resource-language page.
func resLangCount(p resLangPage, code string) int {
	if p.ResourceLanguages == nil {
		return -1
	}
	for _, l := range p.ResourceLanguages.Languages {
		if l.Code == code {
			return l.Works
		}
	}
	return 0
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

// TestAuditSnapshots drives the 384/398 history backbone: POST records
// today's report for the scope (idempotent per day), GET returns the dated
// series per scope, and scopes do not bleed into each other.
func TestAuditSnapshots(t *testing.T) {
	h, bs := newRecordsAPI(t)
	seedAuditWork(t, bs, "waudit00003a", "", "", "Lesbians", map[string]string{"inQll": "true"})
	seedAuditWork(t, bs, "waudit00003b", "", "", "Immigrants", nil)

	rec := request(t, h, http.MethodPost, "/v1/audit/diversity/snapshots", "lib-token", "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("record snapshot = %d %s", rec.Code, rec.Body.String())
	}
	// Same day, same scope: idempotent re-press.
	if rec := request(t, h, http.MethodPost, "/v1/audit/diversity/snapshots", "lib-token", "", nil); rec.Code != http.StatusCreated {
		t.Fatalf("re-record = %d", rec.Code)
	}
	// A scoped snapshot lands in its own series.
	if rec := request(t, h, http.MethodPost, "/v1/audit/diversity/snapshots?filter=inQll%3Dtrue", "lib-token", "", nil); rec.Code != http.StatusCreated {
		t.Fatalf("scoped record = %d %s", rec.Code, rec.Body.String())
	}

	var list struct {
		Snapshots []struct {
			Date         string `json:"date"`
			TotalWorks   int    `json:"totalWorks"`
			Multiplicity struct {
				MatchedOne int `json:"matchedOne"`
			} `json:"multiplicity"`
		} `json:"snapshots"`
	}
	rec = request(t, h, http.MethodGet, "/v1/audit/diversity/snapshots", "lib-token", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Snapshots) != 1 {
		t.Fatalf("unfiltered series = %d snapshots, want 1 (same-day idempotent)", len(list.Snapshots))
	}
	if s := list.Snapshots[0]; s.Date == "" || s.TotalWorks != 2 || s.Multiplicity.MatchedOne != 2 {
		t.Errorf("snapshot = %+v, want dated, 2 works, multiplicity carried", s)
	}

	rec = request(t, h, http.MethodGet, "/v1/audit/diversity/snapshots?filter=inQll%3Dtrue", "lib-token", "", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Snapshots) != 1 || list.Snapshots[0].TotalWorks != 1 {
		t.Fatalf("scoped series = %+v, want its own single 1-work snapshot", list.Snapshots)
	}
}
