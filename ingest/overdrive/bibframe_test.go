package overdrive

import (
	"strings"
	"testing"

	"github.com/freeeve/libcatalog/identity"
	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/rdf"
)

// TestBIBFRAMECrosswalk checks the direct JSON->BIBFRAME mapping of the sample
// item, both the intermediate BIBFRAME struct and the serialized RDF graph.
func TestBIBFRAMECrosswalk(t *testing.T) {
	it := sampleItem()
	bib := it.BIBFRAME()

	if bib.Work.Class != "Audio" {
		t.Errorf("Work.Class = %q, want Audio for an audiobook", bib.Work.Class)
	}
	if n := len(bib.Work.Titles); n != 1 || bib.Work.Titles[0].MainTitle != "Herculine" ||
		bib.Work.Titles[0].Subtitle != "A Novel" {
		t.Errorf("Work.Titles = %+v", bib.Work.Titles)
	}
	if n := len(bib.Work.Subjects); n != 2 {
		t.Fatalf("Work.Subjects count = %d, want 2", n)
	}
	for _, s := range bib.Work.Subjects {
		if s.Class != "Topic" {
			t.Errorf("subject %q class = %q, want Topic", s.Label, s.Class)
		}
	}
	if got := bib.Work.Languages; len(got) != 1 || got[0] != "eng" {
		t.Errorf("Work.Languages = %v, want [eng]", got)
	}
	if c := bib.Work.Classifications; len(c) != 1 || c[0].Value != "FIC073000" || c[0].Source != SourceBISAC ||
		c[0].Label != "Fiction / LGBTQ+ / Transgender" {
		t.Errorf("Work.Classifications = %+v, want one BISAC code with source %q and the heading label (tasks/142)", bib.Work.Classifications, SourceBISAC)
	}

	if n := len(bib.Work.Contributions); n != 2 {
		t.Fatalf("Contributions count = %d, want 2", n)
	}
	if c := bib.Work.Contributions[0]; !c.Primary || c.Label != "Byron, Grace" || !hasRole(c, relatorVocab+"aut", "author") {
		t.Errorf("primary contribution = %+v", c)
	}
	if c := bib.Work.Contributions[1]; c.Primary || c.Label != "Endres, Nicky" || !hasRole(c, relatorVocab+"nrt", "narrator") {
		t.Errorf("narrator contribution = %+v", c)
	}

	if bib.Instance.EditionStatement != "Unabridged" {
		t.Errorf("EditionStatement = %q", bib.Instance.EditionStatement)
	}
	// Format lives on the Instance (tasks/011): an audiobook carries RDA media "audio"
	// and an online-resource carrier, so the projector can facet it independently of
	// the (clustered) Work class.
	if m := bib.Instance.Media; len(m) != 1 || m[0].Label != "audio" || m[0].Code != "s" {
		t.Errorf("Instance media = %+v, want [{s audio}]", bib.Instance.Media)
	}
	if c := bib.Instance.Carrier; len(c) != 1 || c[0].Label != "online resource" || c[0].Code != "cr" {
		t.Errorf("Instance carrier = %+v, want [{cr online resource}]", bib.Instance.Carrier)
	}
	if ps := bib.Instance.Provisions; len(ps) != 1 || ps[0].Class != "Publication" || ps[0].Publisher != "Simon & Schuster Audio" || ps[0].Date != "2025" {
		t.Errorf("Provisions = %+v", bib.Instance.Provisions)
	}
	if !hasIdentifier(bib.Instance.Identifiers, "Isbn", "9781668128251", "") {
		t.Error("missing ISBN identifier")
	}
	if !hasIdentifier(bib.Instance.Identifiers, "Identifier", it.ID, SourceOverDrive) {
		t.Error("missing OverDrive title-id identifier with source overdrive")
	}
	if !hasIdentifier(bib.Instance.Identifiers, "Identifier", it.ReserveID, SourceReserveID) {
		t.Error("missing Reserve ID identifier with source overdrive-reserve")
	}
	if got := it.WorkID(); got != "11682058" {
		t.Errorf("WorkID = %q, want 11682058", got)
	}

	// The serialized graph must carry the audiobook class, the LC language URI,
	// both topical subjects, the ISBN, and the bf:source scheme labels (tasks/008),
	// all in the feed:overdrive graph.
	nq := string(bib.Graph(it.WorkID()).NQuads(rdf.NewIRI("feed:overdrive")))
	for _, want := range []string{
		"http://id.loc.gov/ontologies/bibframe/Audio",
		"http://id.loc.gov/ontologies/bibframe/media", // per-Instance format (tasks/011)
		"http://id.loc.gov/vocabulary/languages/eng",
		"9781668128251",
		"feed:overdrive",
		SourceBISAC,                      // bf:source "bisacsh" on the BISAC classification
		"Fiction / LGBTQ+ / Transgender", // BISAC heading as rdfs:label on the node (tasks/142)
		SourceReserveID,                  // bf:source "overdrive-reserve" on the Reserve ID
		"http://id.loc.gov/ontologies/bibframe/source",
		relatorVocab + "aut", // relator IRI on the primary contribution (tasks/019)
		relatorVocab + "nrt", // relator IRI on the narrator contribution (tasks/019)
	} {
		if !strings.Contains(nq, want) {
			t.Errorf("n-quads missing %q", want)
		}
	}
	if n := strings.Count(nq, "http://id.loc.gov/ontologies/bibframe/Topic"); n != 2 {
		t.Errorf("bf:Topic count = %d, want 2", n)
	}
}

// TestIdentityRoundTrip proves the derive-from-grains model is consistent: the
// provider keys ScanGrain recovers from a written grain must equal the keys the
// ingest path resolves by, or a re-ingest would fail to find the committed
// Instance and would churn its id.
func TestIdentityRoundTrip(t *testing.T) {
	it := sampleItem()
	grain := it.BIBFRAME().Graph(it.WorkID()).NQuads(rdf.NewIRI("feed:overdrive"))

	gi, err := identity.ScanGrain(grain)
	if err != nil {
		t.Fatalf("ScanGrain: %v", err)
	}
	if len(gi.Instances) != 1 {
		t.Fatalf("recovered %d instances, want 1", len(gi.Instances))
	}
	inst := gi.Instances[0]
	scanned, ingest := toSet(inst.ProviderKeys), toSet(it.Identity().ProviderKeys)
	for k := range ingest {
		if !scanned[k] {
			t.Errorf("ingest key %q not recovered from grain (keys: %v)", k, inst.ProviderKeys)
		}
	}
	for k := range scanned {
		if !ingest[k] {
			t.Errorf("grain key %q not produced by ingest", k)
		}
	}
	// A single-item grain shares the base between Work and Instance.
	if inst.InstanceID != it.WorkID() || inst.WorkID != it.WorkID() {
		t.Errorf("ids = %+v, want both = %q", inst, it.WorkID())
	}
	// The recovered cluster key must match what ingest would compute.
	rec := it.Identity()
	if want := identity.WorkKey(rec.Author, rec.Title, rec.Lang); len(gi.Works) != 1 || gi.Works[0].ClusterKey != want {
		t.Errorf("work cluster key = %+v, want %q", gi.Works, want)
	}
}

// hasRole reports whether the contribution carries a role with the given relator IRI
// and term -- the direct-to-BIBFRAME path mirrors the MARC $4 relator (tasks/019).
func hasRole(c codexbf.Contribution, iri, term string) bool {
	for _, r := range c.Roles {
		if r.IRI == iri && r.Term == term {
			return true
		}
	}
	return false
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

func hasIdentifier(ids []codexbf.Identifier, class, value, source string) bool {
	for _, id := range ids {
		if id.Class == class && id.Value == value && id.Source == source {
			return true
		}
	}
	return false
}
