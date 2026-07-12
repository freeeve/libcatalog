package ingest_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/ingest/overdrive"
	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/rdf"
)

// stubRecord is a minimal ingest.Record: enough identity keys to resolve and enough
// BIBFRAME to serialize. It stands in for a deployment-authored provider's record,
// proving the pipeline is provider-agnostic.
type stubRecord struct {
	id, author, title, lang, isbn string
	// localID persists the durable id as a bf:Local identifier so a re-ingest
	// recovers its provider key from the grain (as the coll feed does); off by
	// default so existing tests keep their grain bytes.
	localID bool
}

func (r stubRecord) Identity() identity.Record {
	rec := identity.Record{Author: r.author, Title: r.title, Lang: r.lang}
	rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeID, r.id))
	if r.isbn != "" {
		rec.ProviderKeys = append(rec.ProviderKeys, identity.ProviderKey(identity.SchemeISBN, r.isbn))
	}
	return rec
}

func (r stubRecord) Work() codexbf.Work {
	return codexbf.Work{
		Class:  "Text",
		Titles: []codexbf.Title{{MainTitle: r.title}},
		Contributions: []codexbf.Contribution{
			{Primary: true, Class: "Person", Label: r.author, Roles: []codexbf.Role{{Term: "author"}}},
		},
		Languages: []string{r.lang},
	}
}

func (r stubRecord) Instance() codexbf.Instance {
	inst := codexbf.Instance{Titles: []codexbf.Title{{MainTitle: r.title}}}
	if r.isbn != "" {
		inst.Identifiers = append(inst.Identifiers, codexbf.Identifier{Class: "Isbn", Value: r.isbn})
	}
	if r.localID {
		inst.Identifiers = append(inst.Identifiers, codexbf.Identifier{Class: "Local", Value: r.id})
	}
	return inst
}

// stubProvider is a deployment-authored provider built from records held in memory.
type stubProvider struct {
	feed string
	role ingest.Role
	recs []ingest.Record
}

func (p stubProvider) Name() string                                     { return p.feed }
func (p stubProvider) Role() ingest.Role                                { return p.role }
func (p stubProvider) Records(context.Context) ([]ingest.Record, error) { return p.recs, nil }

func stubFactory(recs []ingest.Record) ingest.Factory {
	return func(cfg ingest.Config) (ingest.Provider, error) {
		return stubProvider{feed: cfg.Feed, role: ingest.RoleIngest, recs: recs}, nil
	}
}

// stubMergeProvider is a stubProvider that also declares feed cluster-merges
// (ingest.MergeSeeder), standing in for a coll feed's dcterms:isReplacedBy.
type stubMergeProvider struct {
	stubProvider
	merges []ingest.MergeSeed
}

func (p stubMergeProvider) MergeSeeds() []ingest.MergeSeed { return p.merges }

// TestFeedMergeRetiresBareCluster is the regression: a coll feed folds a
// single-format cluster (coll:5, indexed only as its format bucket coll:5:ebook)
// into another (coll:9). The bare cluster keys the merge names must resolve through
// their format buckets (the WorkForProviderKey fix), and the folded cluster's stale
// grain must then be retired (the resolver-merge retirement fix) -- so its
// identifiers stop duplicating the survivor's.
func TestFeedMergeRetiresBareCluster(t *testing.T) {
	out := t.TempDir()
	v1 := []ingest.Record{
		stubRecord{id: "coll:5:ebook", author: "Stevenson", title: "Nimona", lang: "eng", isbn: "9780062278241", localID: true},
		stubRecord{id: "coll:9:physical", author: "Other", title: "Other Book", lang: "eng", localID: true},
	}
	if _, err := ingest.Run(stubProvider{feed: "coll", role: ingest.RoleIngest, recs: v1}, out); err != nil {
		t.Fatalf("v1 ingest: %v", err)
	}
	if n := countGrains(t, out); n != 2 {
		t.Fatalf("after v1: %d grains, want 2", n)
	}

	// v2: the feed folds coll:5 into coll:9 and no longer carries coll:5's records.
	prov := stubMergeProvider{
		stubProvider: stubProvider{feed: "coll", role: ingest.RoleIngest, recs: []ingest.Record{v1[1]}},
		merges:       []ingest.MergeSeed{{FromKey: "id:coll:5", ToKey: "id:coll:9"}},
	}
	res, err := ingest.Run(prov, out)
	if err != nil {
		t.Fatalf("v2 ingest: %v", err)
	}
	if res.Retired != 1 {
		t.Errorf("Retired = %d, want 1 (the folded coll:5 cluster's grain)", res.Retired)
	}
	if n := countGrains(t, out); n != 1 {
		t.Errorf("after v2: %d grains, want 1 (coll:5 folded into coll:9)", n)
	}
}

// countGrains counts .nq grain files under a projected out dir.
func countGrains(t *testing.T, out string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(filepath.Join(out, "data", "works"), func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".nq") {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk grains: %v", err)
	}
	return n
}

// TestRegistryComposition covers registration: a first-party factory (OverDrive) and
// a custom stub coexist, keys are sorted, duplicates and unknowns error, and the
// registry key defaults the provenance feed.
func TestRegistryComposition(t *testing.T) {
	reg := ingest.NewRegistry()
	if err := reg.Register(overdrive.ProviderName, overdrive.New); err != nil {
		t.Fatalf("register overdrive: %v", err)
	}
	if err := reg.Register("acme", stubFactory(nil)); err != nil {
		t.Fatalf("register acme: %v", err)
	}

	if got, want := strings.Join(reg.Names(), ","), "acme,overdrive"; got != want {
		t.Errorf("Names() = %q, want %q", got, want)
	}
	if err := reg.Register("acme", stubFactory(nil)); err == nil {
		t.Error("duplicate Register(acme) should error")
	}
	if err := reg.Register("", stubFactory(nil)); err == nil {
		t.Error("Register with empty name should error")
	}
	if err := reg.Register("nilfac", nil); err == nil {
		t.Error("Register with nil factory should error")
	}
	if _, err := reg.New("nope", ingest.Config{}); err == nil {
		t.Error("New for unknown provider should error")
	}

	// An empty Config.Feed defaults the provenance graph to the registry key.
	prov, err := reg.New("acme", ingest.Config{})
	if err != nil {
		t.Fatalf("New(acme): %v", err)
	}
	if prov.Name() != "acme" {
		t.Errorf("default feed = %q, want acme", prov.Name())
	}
	// An explicit Config.Feed overrides it.
	prov, err = reg.New("acme", ingest.Config{Feed: "acme-mirror"})
	if err != nil {
		t.Fatalf("New(acme, feed override): %v", err)
	}
	if prov.Name() != "acme-mirror" {
		t.Errorf("overridden feed = %q, want acme-mirror", prov.Name())
	}
}

// TestRunGraphRouting proves the shared pipeline tags a provider's statements with
// its own feed:<name> graph and never another's -- the provenance contract that
// lets providers coexist (ARCHITECTURE §5/§9).
func TestRunGraphRouting(t *testing.T) {
	recs := []ingest.Record{
		stubRecord{id: "a1", author: "Doe, Jane", title: "Alpha", lang: "eng", isbn: "9780000000001"},
		stubRecord{id: "a2", author: "Roe, Rick", title: "Beta", lang: "eng", isbn: "9780000000002"},
	}
	reg := ingest.NewRegistry()
	if err := reg.Register("acme", stubFactory(recs)); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	prov, err := reg.New("acme", ingest.Config{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := ingest.Run(prov, out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.Grains != 2 {
		t.Errorf("grains = %d, want 2", res.Stats.Grains)
	}
	if res.MintedWorks != 2 || res.MintedInstances != 2 {
		t.Errorf("minted works/instances = %d/%d, want 2/2", res.MintedWorks, res.MintedInstances)
	}

	nq := readNQuads(t, out)
	if !strings.Contains(nq, "<feed:acme>") {
		t.Errorf("grains missing feed:acme graph:\n%s", nq)
	}
	if strings.Contains(nq, "<feed:overdrive>") {
		t.Errorf("grains leaked a foreign feed graph:\n%s", nq)
	}
	if !strings.Contains(nq, "Alpha") || !strings.Contains(nq, "Beta") {
		t.Errorf("grains missing expected titles:\n%s", nq)
	}
}

// extraStub is a stubRecord that also carries adopter display extras, exercising the
// optional ingest.ExtraProvider path.
type extraStub struct {
	stubRecord
	extras map[string]string
}

func (r extraStub) Extras() map[string]string { return r.extras }

type termStub struct {
	stubRecord
	subjects []ingest.AuthoritySubject
	terms    []ingest.AuthoritySubject
}

func (r termStub) ControlledSubjects() []ingest.AuthoritySubject { return r.subjects }
func (r termStub) DescribedTerms() []ingest.AuthoritySubject     { return r.terms }

// TestRunDescribedTerms proves a Record implementing TermDescriber has its
// standalone term descriptions (ancestor-chain labels + hierarchy) emitted
// into the feed graph WITHOUT a bf:subject link -- the projection's
// term sideband then labels hierarchy nodes no Work carries.
func TestRunDescribedTerms(t *testing.T) {
	recs := []ingest.Record{
		termStub{
			stubRecord: stubRecord{id: "t1", author: "Doe, Jane", title: "Alpha", lang: "eng", isbn: "9780000000001"},
			subjects: []ingest.AuthoritySubject{{
				URI:     "https://vocab.example.org/child",
				Labels:  map[string]string{"en": "Trans women"},
				Broader: []string{"https://vocab.example.org/parent"},
			}},
			terms: []ingest.AuthoritySubject{{
				URI:     "https://vocab.example.org/parent",
				Labels:  map[string]string{"en": "Gender minorities"},
				Broader: []string{"https://vocab.example.org/grand"},
			}},
		},
	}
	reg := ingest.NewRegistry()
	if err := reg.Register("acme", stubFactory(recs)); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	prov, err := reg.New("acme", ingest.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.Run(prov, out); err != nil {
		t.Fatalf("Run: %v", err)
	}

	nq := readNQuads(t, out)
	for _, want := range []string{
		"bibframe/subject> <https://vocab.example.org/child>",
		`<https://vocab.example.org/parent> <http://www.w3.org/2004/02/skos/core#prefLabel> "Gender minorities"@en <feed:acme>`,
		"<https://vocab.example.org/parent> <http://www.w3.org/2004/02/skos/core#broader> <https://vocab.example.org/grand>",
	} {
		if !strings.Contains(nq, want) {
			t.Errorf("grains missing %q:\n%s", want, nq)
		}
	}
	// Description only: the ancestor is never linked as a work subject.
	if strings.Contains(nq, "bibframe/subject> <https://vocab.example.org/parent>") {
		t.Errorf("described term linked as a work subject:\n%s", nq)
	}
}

// TestRunWorkExtras proves a Record implementing ExtraProvider has its non-BIBFRAME
// display fields emitted into the Work's feed provenance graph under bibframe.ExtraPred,
// so the projector can surface them as catalog.json's `extra`. A record that
// does not implement ExtraProvider (plain stubRecord) emits no such statements.
func TestRunWorkExtras(t *testing.T) {
	recs := []ingest.Record{
		extraStub{
			stubRecord: stubRecord{id: "e1", author: "Doe, Jane", title: "Alpha", lang: "eng", isbn: "9780000000001"},
			extras:     map[string]string{"cover": "https://covers.example.org/a.jpg", "rating": "4"},
		},
		stubRecord{id: "e2", author: "Roe, Rick", title: "Beta", lang: "eng", isbn: "9780000000002"},
	}
	reg := ingest.NewRegistry()
	if err := reg.Register("acme", stubFactory(recs)); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	prov, err := reg.New("acme", ingest.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingest.Run(prov, out); err != nil {
		t.Fatalf("Run: %v", err)
	}

	nq := readNQuads(t, out)
	const extraNS = "https://github.com/freeeve/libcat/ns#extra/"
	for _, want := range []string{
		extraNS + "cover> \"https://covers.example.org/a.jpg\"",
		extraNS + "rating> \"4\"",
	} {
		if !strings.Contains(nq, want) {
			t.Errorf("grains missing extra statement %q:\n%s", want, nq)
		}
	}
	// The extras belong to the feed provenance graph, not editorial.
	if strings.Contains(nq, extraNS) && !strings.Contains(nq, "<feed:acme>") {
		t.Errorf("extras not in the feed graph:\n%s", nq)
	}
	// A record without ExtraProvider emits no extra predicate for its Work.
	if strings.Contains(nq, "Beta") && strings.Count(nq, extraNS) != 2 {
		t.Errorf("expected exactly the two extras from e1, got %d occurrences:\n%s", strings.Count(nq, extraNS), nq)
	}
}

// TestRunReingestStable proves the pipeline is derive-from-grains: a second run over
// the same records seeds ids from the committed grains, mints nothing, and rewrites
// byte-identical grains (the no-churn gate, now exercised generically).
func TestRunReingestStable(t *testing.T) {
	recs := []ingest.Record{
		stubRecord{id: "a1", author: "Doe, Jane", title: "Alpha", lang: "eng", isbn: "9780000000001"},
	}
	prov := stubProvider{feed: "acme", role: ingest.RoleIngest, recs: recs}
	out := t.TempDir()

	first, err := ingest.Run(prov, out)
	if err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if first.MintedWorks == 0 {
		t.Fatal("first run minted no works")
	}
	before := readNQuads(t, out)

	second, err := ingest.Run(prov, out)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if second.MintedWorks != 0 || second.MintedInstances != 0 {
		t.Errorf("re-ingest minted %d works, %d instances; want 0/0",
			second.MintedWorks, second.MintedInstances)
	}
	if after := readNQuads(t, out); after != before {
		t.Errorf("re-ingest changed grains:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestRunRejectsEnrichRole confirms Run executes only ingest-role providers today;
// an enrichment provider is refused rather than silently ingested.
func TestRunRejectsEnrichRole(t *testing.T) {
	prov := stubProvider{feed: "authority", role: ingest.RoleEnrich}
	if _, err := ingest.Run(prov, t.TempDir()); err == nil {
		t.Error("Run should reject a non-ingest provider")
	}
}

// TestOverdriveProviderThroughRegistry runs the real first-party provider end-to-end
// through the registry over a minimal page cache, proving the built-in factory plugs
// into the same pipeline and routes to feed:overdrive.
func TestOverdriveProviderThroughRegistry(t *testing.T) {
	cache := t.TempDir()
	page := `{"items":[{"id":"12345","title":"Registry Test","creators":[{"name":"Doe, Jane","role":"Author","sortName":"Doe, Jane"}],"languages":[{"id":"en","name":"English"}],"formats":[{"identifiers":[{"type":"ISBN","value":"9780000000009"}]}]}]}`
	if err := os.WriteFile(filepath.Join(cache, "page-0001.json"), []byte(page), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := ingest.NewRegistry()
	if err := reg.Register(overdrive.ProviderName, overdrive.New); err != nil {
		t.Fatal(err)
	}
	prov, err := reg.New(overdrive.ProviderName, ingest.Config{Source: cache})
	if err != nil {
		t.Fatalf("New(overdrive): %v", err)
	}
	out := t.TempDir()
	res, err := ingest.Run(prov, out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stats.Grains != 1 {
		t.Errorf("grains = %d, want 1", res.Stats.Grains)
	}
	nq := readNQuads(t, out)
	if !strings.Contains(nq, "<feed:overdrive>") {
		t.Errorf("overdrive grains missing feed:overdrive graph:\n%s", nq)
	}
	if !strings.Contains(nq, "Registry Test") {
		t.Errorf("overdrive grains missing title:\n%s", nq)
	}
}

// TestRunAddedRecordsMintOnlyNew is the acceptance for a *changed* feed:
// re-ingesting a feed with a new record added preserves the ids (and byte-identical
// grains) of the unchanged records and mints only the genuinely new one.
func TestRunAddedRecordsMintOnlyNew(t *testing.T) {
	v1 := []ingest.Record{
		stubRecord{id: "a1", author: "Doe, Jane", title: "Alpha", lang: "eng", isbn: "9780000000001"},
		stubRecord{id: "a2", author: "Roe, Rick", title: "Beta", lang: "eng", isbn: "9780000000002"},
	}
	out := t.TempDir()
	if _, err := ingest.Run(stubProvider{feed: "acme", role: ingest.RoleIngest, recs: v1}, out); err != nil {
		t.Fatalf("v1 Run: %v", err)
	}
	before := grainFiles(t, out)
	if len(before) != 2 {
		t.Fatalf("v1 grains = %d, want 2", len(before))
	}

	v2 := append(append([]ingest.Record{}, v1...),
		stubRecord{id: "a3", author: "Poe, Ann", title: "Gamma", lang: "eng", isbn: "9780000000003"})
	res, err := ingest.Run(stubProvider{feed: "acme", role: ingest.RoleIngest, recs: v2}, out)
	if err != nil {
		t.Fatalf("v2 Run: %v", err)
	}
	if res.MintedWorks != 1 || res.MintedInstances != 1 {
		t.Errorf("changed feed minted %d/%d, want 1/1 (only the added record)", res.MintedWorks, res.MintedInstances)
	}
	after := grainFiles(t, out)
	if len(after) != 3 {
		t.Errorf("v2 grains = %d, want 3", len(after))
	}
	// Every prior grain persists byte-identical -> unchanged records did not churn.
	for path, b := range before {
		ab, ok := after[path]
		if !ok {
			t.Errorf("prior grain %s vanished (id churn)", path)
		} else if !bytes.Equal(ab, b) {
			t.Errorf("prior grain %s changed for an unchanged record", path)
		}
	}
}

// TestRunChangedRecordKeepsId proves a record whose content changes (new title) but
// whose keys are stable resolves to its committed ids -- 0 minted, no new grain file,
// only the content updates. This is the "preserves ids" half of the contract: identity
// survives feed edits because the ISBN anchors it, not the (title-derived) cluster key.
func TestRunChangedRecordKeepsId(t *testing.T) {
	out := t.TempDir()
	v1 := []ingest.Record{stubRecord{id: "a1", author: "Doe, Jane", title: "Original Title", lang: "eng", isbn: "9780000000001"}}
	if _, err := ingest.Run(stubProvider{feed: "acme", role: ingest.RoleIngest, recs: v1}, out); err != nil {
		t.Fatalf("v1 Run: %v", err)
	}
	before := grainFiles(t, out)
	if len(before) != 1 {
		t.Fatalf("v1 grains = %d, want 1", len(before))
	}
	var path string
	var orig []byte
	for p, b := range before {
		path, orig = p, b
	}

	v2 := []ingest.Record{stubRecord{id: "a1", author: "Doe, Jane", title: "Revised Title", lang: "eng", isbn: "9780000000001"}}
	res, err := ingest.Run(stubProvider{feed: "acme", role: ingest.RoleIngest, recs: v2}, out)
	if err != nil {
		t.Fatalf("v2 Run: %v", err)
	}
	if res.MintedWorks != 0 || res.MintedInstances != 0 {
		t.Errorf("changed record minted %d/%d, want 0/0 (id preserved)", res.MintedWorks, res.MintedInstances)
	}
	after := grainFiles(t, out)
	if len(after) != 1 {
		t.Errorf("grains = %d, want 1 (no new file for a changed record)", len(after))
	}
	ab, ok := after[path]
	if !ok {
		t.Fatalf("grain %s vanished -> id churned on a content change", path)
	}
	if bytes.Equal(ab, orig) {
		t.Error("grain content did not update for the changed record")
	}
	if !strings.Contains(string(ab), "Revised Title") {
		t.Error("changed grain missing the new title")
	}
}

// grainFiles maps each per-Work grain's dir-relative path to its bytes (skipping the
// bulk catalog.nq), so a test can detect which grains persisted, changed, or appeared.
func grainFiles(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".nq") || d.Name() == "catalog.nq" {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = b
		return nil
	})
	if err != nil {
		t.Fatalf("walk grains: %v", err)
	}
	return out
}

// readNQuads returns the concatenated contents of every per-Work grain under dir
// (skipping the bulk catalog.nq), so a test can assert on provenance graphs.
func readNQuads(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".nq") || d.Name() == "catalog.nq" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		b.Write(data)
		return nil
	})
	if err != nil {
		t.Fatalf("read grains: %v", err)
	}
	return b.String()
}

// TestTwoFeedGrainBlanksStayDisjoint is the tasks/397 regression: a second
// feed's re-ingest preserves the first feed's graph, and the preserved blanks
// must never fuse with the fresh feed's. The corruption this guards against:
// one blank label serialized into two graphs makes the parser merge unrelated
// nodes, so a subject absorbs a summary or classification (seen live: the
// playground's American Hippo grain).
func TestTwoFeedGrainBlanksStayDisjoint(t *testing.T) {
	out := t.TempDir()
	recA := []ingest.Record{stubRecord{id: "a:1", author: "Gailey, Sarah", title: "American Hippo", lang: "eng", isbn: "9781250176431", localID: true}}
	recB := []ingest.Record{stubRecord{id: "b:1", author: "Gailey, Sarah", title: "American Hippo", lang: "eng", isbn: "9781250176431", localID: true}}
	if _, err := ingest.Run(stubProvider{feed: "feeda", role: ingest.RoleIngest, recs: recA}, out); err != nil {
		t.Fatalf("feed A: %v", err)
	}
	if _, err := ingest.Run(stubProvider{feed: "feedb", role: ingest.RoleIngest, recs: recB}, out); err != nil {
		t.Fatalf("feed B: %v", err)
	}

	// One work, one grain, two feed graphs. No blank node may be typed in
	// more than one graph -- that is the fusion signature.
	grains := findGrains(t, out)
	if len(grains) != 1 {
		t.Fatalf("grains = %d, want 1 (same work clustered)", len(grains))
	}
	data, err := os.ReadFile(grains[0])
	if err != nil {
		t.Fatal(err)
	}
	assertBlanksSingleGraph(t, data)

	// Third pass (feed A again) exercises preservation of a multi-graph
	// prior; the invariant must hold transitively.
	if _, err := ingest.Run(stubProvider{feed: "feeda", role: ingest.RoleIngest, recs: recA}, out); err != nil {
		t.Fatalf("feed A re-run: %v", err)
	}
	data, err = os.ReadFile(grains[0])
	if err != nil {
		t.Fatal(err)
	}
	assertBlanksSingleGraph(t, data)
}

// findGrains lists the .nq grain files under out.
func findGrains(t *testing.T, out string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(filepath.Join(out, "data", "works"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".nq") {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return paths
}

// assertBlanksSingleGraph fails if any blank node appears in more than one
// named graph of the grain.
func assertBlanksSingleGraph(t *testing.T, grain []byte) {
	t.Helper()
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		t.Fatal(err)
	}
	graphsOf := map[string]map[string]bool{}
	for _, q := range ds.Quads {
		for _, term := range []rdf.Term{q.S, q.O} {
			if !term.IsBlank() {
				continue
			}
			set := graphsOf[term.Value]
			if set == nil {
				set = map[string]bool{}
				graphsOf[term.Value] = set
			}
			set[q.G.Value] = true
		}
	}
	for label, graphs := range graphsOf {
		if len(graphs) > 1 {
			names := make([]string, 0, len(graphs))
			for g := range graphs {
				names = append(names, g)
			}
			sort.Strings(names)
			t.Errorf("blank _:%s appears in %d graphs (%s) -- fused nodes", label, len(graphs), strings.Join(names, ", "))
		}
	}
}
