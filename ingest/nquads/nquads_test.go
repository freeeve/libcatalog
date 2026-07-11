package nquads

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
)

const testMapping = `
work-prefix = "urn:demo:work:"
id-order = "numeric"
default-language = "eng"

[predicates]
title = "http://purl.org/dc/terms/title"
creator = "http://purl.org/dc/terms/creator"
identifier = "http://purl.org/dc/terms/identifier"
subject = "http://purl.org/dc/terms/subject"
source = "http://purl.org/dc/terms/source"
language = "http://purl.org/dc/terms/language"
prefLabel = "http://www.w3.org/2004/02/skos/core#prefLabel"

[identifiers]
"urn:isbn:" = "isbn"
"urn:demo:od:" = "overdrive"

[languages]
en = "eng"
fr = "fre"

[sources]
prefix = "urn:demo:src:"
tentative = ["urn:demo:src:scan-tier-2"]
`

const testNQ = `<urn:demo:work:2> <http://purl.org/dc/terms/title> "Second Work" .
<urn:demo:work:2> <http://purl.org/dc/terms/creator> "Ada Author" .
<urn:demo:work:2> <http://purl.org/dc/terms/identifier> <urn:isbn:9780000000011> .
<urn:demo:work:2> <http://purl.org/dc/terms/subject> <https://homosaurus.org/v5/hom1> .
<urn:demo:work:2> <http://purl.org/dc/terms/source> <urn:demo:src:loc> .
<urn:demo:work:2> <http://purl.org/dc/terms/language> "fr" .
<urn:demo:work:10> <http://purl.org/dc/terms/title> "Tenth Work" .
<urn:demo:work:10> <http://purl.org/dc/terms/identifier> <urn:demo:od:abc-123> .
<urn:demo:work:10> <http://purl.org/dc/terms/source> <urn:demo:src:scan-tier-2> .
<urn:demo:work:11> <http://purl.org/dc/terms/creator> "No Title" .
<https://homosaurus.org/v5/hom1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Label One" .
`

// buildProvider writes the fixture mapping + export and constructs the provider.
func buildProvider(t *testing.T, params map[string]string) ingest.Provider {
	t.Helper()
	dir := t.TempDir()
	mapping := filepath.Join(dir, "mapping.toml")
	nq := filepath.Join(dir, "export.nq")
	if err := os.WriteFile(mapping, []byte(testMapping), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nq, []byte(testNQ), 0o644); err != nil {
		t.Fatal(err)
	}
	if params == nil {
		params = map[string]string{}
	}
	params["mapping"] = mapping
	p, err := New(ingest.Config{Source: nq, Params: params})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRecordsMappedFields checks the mapping drives every field: titles,
// creators (last-first), identifier schemes, subjects with harvested labels,
// language table, source slugs with the tentative marker, and numeric order.
func TestRecordsMappedFields(t *testing.T) {
	recs, err := buildProvider(t, nil).Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("records = %d, want 2 (untitled work dropped)", len(recs))
	}

	// Numeric id-order: 2 before 10.
	id0 := recs[0].Identity()
	if !strings.Contains(id0.Author, "nquads:2") || id0.Title != "Second Work" {
		t.Fatalf("first record identity = %+v", id0)
	}
	if id0.Lang != "fre" {
		t.Fatalf("language table not applied: %q", id0.Lang)
	}
	keys := map[string]bool{}
	for _, k := range id0.ProviderKeys {
		keys[string(k)] = true
	}
	if !keys[string(identity.ProviderKey(identity.SchemeISBN, "9780000000011"))] {
		t.Fatalf("missing isbn key: %v", id0.ProviderKeys)
	}

	w := recs[0].Work()
	if len(w.Contributions) != 1 || w.Contributions[0].Label != "Author, Ada" {
		t.Fatalf("contributions = %+v", w.Contributions)
	}
	subs := recs[0].(record).ControlledSubjects()
	if len(subs) != 1 || subs[0].Labels["en"] != "Label One" {
		t.Fatalf("subjects = %+v", subs)
	}
	extras := recs[0].(record).Extras()
	if extras["sources"] != "loc" || extras["tentative"] != "" {
		t.Fatalf("extras = %v", extras)
	}

	// The tier-2-only work: schemed id key, tentative extra.
	id1 := recs[1].Identity()
	if !slices.Contains(id1.ProviderKeys, string(identity.ProviderKey(identity.SchemeID, "overdrive:abc-123"))) {
		t.Fatalf("missing schemed id key: %v", id1.ProviderKeys)
	}
	extras1 := recs[1].(record).Extras()
	if extras1["tentative"] != "yes" || extras1["sources"] != "scan-tier-2" {
		t.Fatalf("tentative extras = %v", extras1)
	}
}

// TestTentativeDrop checks Params["tentative"]="drop" removes works whose
// only attestation is a tentative source.
func TestTentativeDrop(t *testing.T) {
	recs, err := buildProvider(t, map[string]string{"tentative": "drop"}).Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Identity().Title != "Second Work" {
		t.Fatalf("records = %d, want only the confident work", len(recs))
	}
}

// TestMappingValidation checks the mapping loader rejects broken profiles.
func TestMappingValidation(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"missing work-prefix": `[predicates]
title = "x"`,
		"no predicates": `work-prefix = "urn:w:"`,
		"unknown field": `work-prefix = "urn:w:"
[predicates]
banana = "x"`,
		"bad id-order": `work-prefix = "urn:w:"
id-order = "random"
[predicates]
title = "x"`,
	}
	for name, body := range cases {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".toml")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadMapping(path); err == nil {
			t.Errorf("%s: mapping accepted, want error", name)
		}
	}
}

// collMapping is a coll-feed-shaped mapping: bucket grouping,
// contributor roles, provisions, format, tags/keywords, classifications,
// extras passthrough, non-key identifier rules, broader harvesting, and a
// fixed identity language.
const collMapping = `
work-prefix = "urn:coll:work:"
id-scheme = "coll"
id-order = "numeric"
identity-language = "eng"
keyword-source = "overdrive"
extras-prefix = "urn:coll:extra:"

[predicates]
title = "http://purl.org/dc/terms/title"
subtitle = "https://schema.org/alternativeHeadline"
creator = "http://purl.org/dc/terms/creator"
contributor = "http://purl.org/dc/terms/contributor"
summary = "http://purl.org/dc/terms/abstract"
identifier = "http://purl.org/dc/terms/identifier"
subject = "http://purl.org/dc/terms/subject"
language = "http://purl.org/dc/terms/language"
publisher = "http://purl.org/dc/terms/publisher"
issued = "http://purl.org/dc/terms/issued"
format = "http://purl.org/dc/terms/format"
tag = "urn:coll:p:qll-tag"
keyword = "https://schema.org/keywords"
classification = "urn:coll:p:classification"
group = "http://purl.org/dc/terms/isPartOf"
prefLabel = "http://www.w3.org/2004/02/skos/core#prefLabel"
broader = "http://www.w3.org/2004/02/skos/core#broader"

[classifications]
prefix = "urn:bisac:"
source = "bisacsh"

[identifiers]
"urn:isbn:" = "isbn"
[identifiers."urn:coll:isbn10:"]
class = "Isbn"
key = false
[identifiers."urn:coll:asin:"]
source = "asin"
key = false
[identifiers."urn:coll:odreserve:"]
source = "overdrive-reserve"
key = false
`

// collNQ is one cluster (7) in three buckets -- physical, ebook, formatless
// -- with work-level statements repeated per bucket per the coll-feed
// contract, plus the term statements: the used subject's labels + broader,
// its labeled ancestor chain, and the BISAC code label.
const collNQ = `<urn:coll:work:7:physical> <http://purl.org/dc/terms/isPartOf> <urn:coll:work:7> .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/title> "Stone Butch Blues" .
<urn:coll:work:7:physical> <https://schema.org/alternativeHeadline> "A Novel" .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/creator> "Leslie Feinberg" .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/contributor> "Feinberg, Leslie (Author)" .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/contributor> "Doe, Jane (Narrator)" .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/abstract> "A classic." .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/language> "fre" .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/language> "eng" .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/subject> <https://homosaurus.org/v5/hom1> .
<urn:coll:work:7:physical> <urn:coll:p:qll-tag> "ebook in QLL" .
<urn:coll:work:7:physical> <https://schema.org/keywords> "lgbtq fiction" .
<urn:coll:work:7:physical> <urn:coll:p:classification> <urn:bisac:FIC000000> .
<urn:coll:work:7:physical> <urn:coll:extra:cover> "https://covers.example.org/7.jpg" .
<urn:coll:work:7:physical> <urn:coll:extra:sources> "QLL,loc" .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/identifier> <urn:isbn:9780000000077> .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/identifier> <urn:coll:isbn10:0000000077> .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/publisher> "Firebrand Books" .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/issued> "1993" .
<urn:coll:work:7:physical> <http://purl.org/dc/terms/format> "physical" .
<urn:coll:work:7:ebook> <http://purl.org/dc/terms/isPartOf> <urn:coll:work:7> .
<urn:coll:work:7:ebook> <http://purl.org/dc/terms/title> "Stone Butch Blues" .
<urn:coll:work:7:ebook> <http://purl.org/dc/terms/creator> "Leslie Feinberg" .
<urn:coll:work:7:ebook> <http://purl.org/dc/terms/subject> <https://homosaurus.org/v5/hom1> .
<urn:coll:work:7:ebook> <http://purl.org/dc/terms/format> "ebook" .
<urn:coll:work:7:ebook> <http://purl.org/dc/terms/identifier> <urn:coll:asin:B00EXAMPLE> .
<urn:coll:work:7:ebook> <http://purl.org/dc/terms/identifier> <urn:coll:odreserve:123456> .
<urn:coll:work:7> <http://purl.org/dc/terms/title> "Stone Butch Blues" .
<urn:coll:work:7> <http://purl.org/dc/terms/creator> "Leslie Feinberg" .
<urn:coll:work:7> <http://purl.org/dc/terms/subject> <https://homosaurus.org/v5/hom1> .
<https://homosaurus.org/v5/hom1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Butch" .
<https://homosaurus.org/v5/hom1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Butch (es)"@es .
<https://homosaurus.org/v5/hom1> <http://www.w3.org/2004/02/skos/core#broader> <https://homosaurus.org/v5/hom2> .
<https://homosaurus.org/v5/hom2> <http://www.w3.org/2004/02/skos/core#prefLabel> "Gender expression" .
<https://homosaurus.org/v5/hom2> <http://www.w3.org/2004/02/skos/core#broader> <https://homosaurus.org/v5/hom3> .
<urn:bisac:FIC000000> <http://www.w3.org/2004/02/skos/core#prefLabel> "Fiction / General" .
`

// TestMergeSeedsFromIsReplacedBy proves the provider surfaces a coll feed's
// dcterms:isReplacedBy cluster-merges as resolver provider keys, keyed
// the same way the records are (SchemeID + the durable id-scheme:id).
func TestMergeSeedsFromIsReplacedBy(t *testing.T) {
	dir := t.TempDir()
	mapping := filepath.Join(dir, "m.toml")
	nq := filepath.Join(dir, "c.nq")
	if err := os.WriteFile(mapping, []byte(collMapping), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `<urn:coll:work:14> <http://purl.org/dc/terms/title> "Nimona" .
<urn:coll:work:14> <http://purl.org/dc/terms/identifier> <urn:isbn:9780062278241> .
<urn:coll:work:51812> <http://purl.org/dc/terms/isReplacedBy> <urn:coll:work:14> .
`
	if err := os.WriteFile(nq, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := New(ingest.Config{Feed: "coll", Source: nq, Params: map[string]string{"mapping": mapping}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Records(context.Background()); err != nil {
		t.Fatal(err)
	}
	ms, ok := p.(ingest.MergeSeeder)
	if !ok {
		t.Fatal("the nquads provider must implement ingest.MergeSeeder")
	}
	want := ingest.MergeSeed{
		FromKey: identity.ProviderKey(identity.SchemeID, "coll:51812"),
		ToKey:   identity.ProviderKey(identity.SchemeID, "coll:14"),
	}
	if seeds := ms.MergeSeeds(); len(seeds) != 1 || seeds[0] != want {
		t.Fatalf("MergeSeeds = %+v, want [%+v]", seeds, want)
	}
}

func buildCollProvider(t *testing.T) ingest.Provider {
	t.Helper()
	dir := t.TempDir()
	mapping := filepath.Join(dir, "coll-mapping.toml")
	nq := filepath.Join(dir, "catalog.coll.nq")
	if err := os.WriteFile(mapping, []byte(collMapping), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nq, []byte(collNQ), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := New(ingest.Config{Feed: "coll", Source: nq, Params: map[string]string{"mapping": mapping}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCollFeedRecords covers the mapping extensions end to end at
// the record level: bucket grouping identity, provider-key round-trips,
// non-key identifiers, provisions, formats, contributions with roles,
// subjects with labels + broader, ancestor terms, classifications with
// harvested labels, tags before keywords, and extras passthrough.
func TestCollFeedRecords(t *testing.T) {
	recs, err := buildCollProvider(t).Records(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 3 {
		t.Fatalf("records = %d, want 3 buckets", len(recs))
	}
	byID := map[string]record{}
	for _, r := range recs {
		rec := r.(record)
		byID[rec.w.id] = rec
	}
	phys, ebook, formatless := byID["7:physical"], byID["7:ebook"], byID["7"]

	// Grouping: all three buckets share the cluster-scoped author key (and
	// the fixed identity language), so they cluster into ONE Work; provider
	// ids stay per-bucket, round-tripping the old provider's keys.
	pid, eid, fid := phys.Identity(), ebook.Identity(), formatless.Identity()
	if pid.Author != "coll:7 Feinberg, Leslie" || pid.Author != eid.Author || pid.Author != fid.Author {
		t.Fatalf("group identity authors = %q / %q / %q", pid.Author, eid.Author, fid.Author)
	}
	if pid.Lang != "eng" {
		t.Fatalf("identity language = %q, want the fixed eng (work carries fre first)", pid.Lang)
	}
	if !slices.Contains(pid.ProviderKeys, string(identity.ProviderKey(identity.SchemeID, "coll:7:physical"))) ||
		!slices.Contains(eid.ProviderKeys, string(identity.ProviderKey(identity.SchemeID, "coll:7:ebook"))) ||
		!slices.Contains(fid.ProviderKeys, string(identity.ProviderKey(identity.SchemeID, "coll:7"))) {
		t.Fatalf("provider keys = %v / %v / %v", pid.ProviderKeys, eid.ProviderKeys, fid.ProviderKeys)
	}
	// isbn13 is a merge key; isbn10/asin/odreserve are not.
	if !slices.Contains(pid.ProviderKeys, string(identity.ProviderKey(identity.SchemeISBN, "9780000000077"))) {
		t.Fatalf("missing isbn merge key: %v", pid.ProviderKeys)
	}
	if len(eid.ProviderKeys) != 1 {
		t.Fatalf("ebook keys = %v, want only the provider id (asin/odreserve are non-key)", eid.ProviderKeys)
	}

	// Work-level mapping: subtitle, contributions with roles, languages in
	// statement order, summary, topics (tag before keyword, keyword
	// sourced), classification with the harvested label.
	w := phys.Work()
	if len(w.Titles) != 1 || w.Titles[0].Subtitle != "A Novel" {
		t.Fatalf("titles = %+v", w.Titles)
	}
	if len(w.Contributions) != 2 || !w.Contributions[0].Primary ||
		w.Contributions[0].Label != "Feinberg, Leslie" || w.Contributions[0].Roles[0].Term != "author" ||
		w.Contributions[1].Label != "Doe, Jane" || w.Contributions[1].Roles[0].Term != "narrator" {
		t.Fatalf("contributions = %+v", w.Contributions)
	}
	if len(w.Languages) != 2 || w.Languages[0] != "fre" || w.Languages[1] != "eng" {
		t.Fatalf("languages = %v, want statement order", w.Languages)
	}
	if len(w.Summary) != 1 || w.Summary[0] != "A classic." {
		t.Fatalf("summary = %v", w.Summary)
	}
	if len(w.Subjects) != 2 || w.Subjects[0].Label != "ebook in QLL" || w.Subjects[0].Source != "" ||
		w.Subjects[1].Label != "lgbtq fiction" || w.Subjects[1].Source != "overdrive" {
		t.Fatalf("topic subjects = %+v", w.Subjects)
	}
	if len(w.Classifications) != 1 || w.Classifications[0].Value != "FIC000000" ||
		w.Classifications[0].Label != "Fiction / General" || w.Classifications[0].Source != "bisacsh" {
		t.Fatalf("classifications = %+v", w.Classifications)
	}

	// Instance-level mapping: RDA media per format, identifier emission
	// (isbn13 + non-key isbn10; raw-valued asin/odreserve), provision.
	pi := phys.Instance()
	if len(pi.Media) != 1 || pi.Media[0].Code != "n" {
		t.Fatalf("physical media = %+v", pi.Media)
	}
	var isbns, others []string
	for _, id := range pi.Identifiers {
		if id.Class == "Isbn" {
			isbns = append(isbns, id.Value)
		} else {
			others = append(others, id.Source+"="+id.Value)
		}
	}
	if !slices.Contains(isbns, "9780000000077") || !slices.Contains(isbns, "0000000077") {
		t.Fatalf("isbn identifiers = %v", isbns)
	}
	if len(pi.Provisions) != 1 || pi.Provisions[0].Publisher != "Firebrand Books" || pi.Provisions[0].Date != "1993" {
		t.Fatalf("provisions = %+v", pi.Provisions)
	}
	ei := ebook.Instance()
	if len(ei.Media) != 1 || ei.Media[0].Code != "c" {
		t.Fatalf("ebook media = %+v", ei.Media)
	}
	var eOthers []string
	for _, id := range ei.Identifiers {
		if id.Class != "Isbn" {
			eOthers = append(eOthers, id.Source+"="+id.Value)
		}
	}
	if !slices.Contains(eOthers, "asin=B00EXAMPLE") || !slices.Contains(eOthers, "overdrive-reserve=123456") ||
		!slices.Contains(eOthers, "coll=coll:7:ebook") {
		t.Fatalf("ebook identifiers = %v", eOthers)
	}
	if fi := formatless.Instance(); len(fi.Media) != 0 {
		t.Fatalf("formatless media = %+v", fi.Media)
	}

	// Term side: the subject carries multilingual labels + broader; the
	// ancestor chain rides DescribedTerms (labeled hom2 with its edge to the
	// undescribed hom3, which itself is skipped); extras pass through with
	// the raw sources value intact.
	subs := phys.ControlledSubjects()
	if len(subs) != 1 || subs[0].Labels["en"] != "Butch" || subs[0].Labels["es"] != "Butch (es)" ||
		len(subs[0].Broader) != 1 || subs[0].Broader[0] != "https://homosaurus.org/v5/hom2" {
		t.Fatalf("controlled subjects = %+v", subs)
	}
	terms := phys.DescribedTerms()
	if len(terms) != 1 || terms[0].URI != "https://homosaurus.org/v5/hom2" ||
		terms[0].Labels["en"] != "Gender expression" ||
		len(terms[0].Broader) != 1 || terms[0].Broader[0] != "https://homosaurus.org/v5/hom3" {
		t.Fatalf("described terms = %+v", terms)
	}
	extras := phys.Extras()
	if extras["cover"] != "https://covers.example.org/7.jpg" || extras["sources"] != "QLL,loc" {
		t.Fatalf("extras = %v", extras)
	}
}

// TestCollFeedGrouping proves the grouping end to end through ingest.Run:
// three bucket records write ONE grain (one Work) carrying three Instances
// with their per-bucket provider ids.
func TestCollFeedGrouping(t *testing.T) {
	out := t.TempDir()
	if _, err := ingest.Run(buildCollProvider(t), out); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var grains []string
	err := filepath.WalkDir(filepath.Join(out, "data", "works"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".nq") {
			grains = append(grains, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(grains) != 1 {
		t.Fatalf("grains = %v, want one Work for the cluster", grains)
	}
	nq, err := os.ReadFile(grains[0])
	if err != nil {
		t.Fatal(err)
	}
	text := string(nq)
	for _, want := range []string{"coll:7:physical", "coll:7:ebook", `"coll:7"`, "Gender expression"} {
		if !strings.Contains(text, want) {
			t.Fatalf("grain missing %q:\n%s", want, text)
		}
	}
}

// TestMappingPredicateList checks a field may list several predicate IRIs.
func TestMappingPredicateList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.toml")
	body := `work-prefix = "urn:w:"
[predicates]
title = ["http://purl.org/dc/terms/title", "http://example.org/title"]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := LoadMapping(path)
	if err != nil {
		t.Fatal(err)
	}
	ff := m.fieldFor()
	if ff["http://purl.org/dc/terms/title"] != "title" || ff["http://example.org/title"] != "title" {
		t.Fatalf("fieldFor = %v", ff)
	}
}

// TestContributionJunkGate covers the policy: both contribution
// paths drop copyright-line debris and overlong non-agent "names" (the
// coll:32780 conference heading), a record whose every agent is junk yields
// no contributions, and the raw creator literal still feeds the identity
// author key.
func TestContributionJunkGate(t *testing.T) {
	longName := strings.Repeat("Zhendėrėės u̇u̇dėltėĭ ", 8) // > 100 bytes, the conference-heading shape
	exactly100 := strings.Repeat("a", 100)

	// Creator fallback: junk creators drop; the first SURVIVOR is primary.
	r := record{w: &work{creators: []string{"© 2011 Somebody", longName, "Ada Author", exactly100}}}
	got := r.contributions()
	if len(got) != 2 || got[0].Label != "Author, Ada" || !got[0].Primary || got[1].Primary {
		t.Fatalf("creator fallback = %+v", got)
	}

	// Every fallback junk pattern drops; an all-junk record has no
	// contributions at all.
	for _, junk := range []string{
		longName,
		"© 2011 EMI Records",
		"1999 EMI Records Ltd.",
		"All Rights Reserved",
		"c",
	} {
		r := record{w: &work{creators: []string{junk}}}
		if got := r.contributions(); len(got) != 0 {
			t.Errorf("creator %q not dropped: %+v", junk, got)
		}
	}

	// Mapped-contributor path: same gate, plus the copyright-holder role;
	// all-junk contributors fall through to a clean creator.
	r = record{w: &work{
		contributors: []string{"Jane Doe (Copyright Holder)", longName + " (Author)", "Doe, Jane (Narrator)"},
	}}
	got = r.contributions()
	if len(got) != 1 || got[0].Label != "Doe, Jane" || got[0].Roles[0].Term != "narrator" || !got[0].Primary {
		t.Fatalf("mapped contributors = %+v", got)
	}
	r = record{w: &work{
		contributors: []string{"Jane Doe (Copyright Holder)"},
		creators:     []string{"Ada Author"},
	}}
	got = r.contributions()
	if len(got) != 1 || got[0].Label != "Author, Ada" {
		t.Fatalf("all-junk contributors did not fall back to creator: %+v", got)
	}

	// Mapped contributor names are final sort-form labels: no
	// lastFirst re-inversion of direct forms, and the year-led junk test
	// exempts comma-bearing inverted names.
	r = record{w: &work{contributors: []string{
		"Barefoot Books (author)",
		"Twin Cities GLBT Oral History Project (author)",
		"5000, Alaska Thunderfuck (narrator)",
		"1999 EMI Records Ltd. (author)",
	}}}
	got = r.contributions()
	if len(got) != 3 || got[0].Label != "Barefoot Books" ||
		got[1].Label != "Twin Cities GLBT Oral History Project" ||
		got[2].Label != "5000, Alaska Thunderfuck" || got[2].Roles[0].Term != "narrator" {
		t.Fatalf("sort-form labels mangled: %+v", got)
	}

	// The creator FALLBACK still lastFirsts (raw access points, not sort
	// forms) and still drops comma-less year-led debris.
	r = record{w: &work{creators: []string{"1999 EMI Records Ltd."}}}
	if got := r.contributions(); len(got) != 0 {
		t.Fatalf("year-led creator debris survived: %+v", got)
	}

	// The identity author key keeps reading the raw creator literal even
	// when the gate drops it as a contribution (the drop must
	// not re-merge distinct works or orphan the key).
	junkOnly := record{
		w: &work{group: "32780", creators: []string{longName}},
		m: &Mapping{}, idScheme: "coll",
	}
	if id := junkOnly.Identity(); !strings.Contains(id.Author, "Zhendėrėės") {
		t.Fatalf("identity author lost the creator literal: %q", id.Author)
	}
	if got := junkOnly.contributions(); len(got) != 0 {
		t.Fatalf("junk-only record grew contributions: %+v", got)
	}
}
