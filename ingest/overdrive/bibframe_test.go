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
	if n := len(bib.Work.Classifications); n != 1 || bib.Work.Classifications[0].Value != "FIC073000" {
		t.Errorf("Work.Classifications = %+v, want one BISAC code (MARC path drops it)", bib.Work.Classifications)
	}

	if n := len(bib.Work.Contributions); n != 2 {
		t.Fatalf("Contributions count = %d, want 2", n)
	}
	if c := bib.Work.Contributions[0]; !c.Primary || c.Label != "Byron, Grace" || c.Role != "author" {
		t.Errorf("primary contribution = %+v", c)
	}
	if c := bib.Work.Contributions[1]; c.Primary || c.Label != "Endres, Nicky" || c.Role != "narrator" {
		t.Errorf("narrator contribution = %+v", c)
	}

	if bib.Instance.EditionStatement != "Unabridged" {
		t.Errorf("EditionStatement = %q", bib.Instance.EditionStatement)
	}
	if p := bib.Instance.Provision; p == nil || p.Publisher != "Simon & Schuster Audio" || p.Date != "2025" {
		t.Errorf("Provision = %+v", p)
	}
	if !hasIdentifier(bib.Instance.Identifiers, "Isbn", "9781668128251") {
		t.Error("missing ISBN identifier")
	}
	if !hasIdentifier(bib.Instance.Identifiers, "Identifier", it.ID) {
		t.Error("missing OverDrive title-id identifier")
	}
	if !hasIdentifier(bib.Instance.Identifiers, "Identifier", it.ReserveID) {
		t.Error("missing Reserve ID identifier")
	}
	if got := it.WorkID(); got != "11682058" {
		t.Errorf("WorkID = %q, want 11682058", got)
	}

	// The serialized graph must carry the audiobook class, the LC language URI,
	// both topical subjects and the ISBN, all in the feed:overdrive graph.
	nq := string(bib.Graph(it.WorkID()).NQuads(rdf.NewIRI("feed:overdrive")))
	for _, want := range []string{
		"http://id.loc.gov/ontologies/bibframe/Audio",
		"http://id.loc.gov/vocabulary/languages/eng",
		"9781668128251",
		"feed:overdrive",
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

	ids, err := identity.ScanGrain(grain)
	if err != nil {
		t.Fatalf("ScanGrain: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("recovered %d instances, want 1", len(ids))
	}
	scanned, ingest := toSet(ids[0].ProviderKeys), toSet(it.Identity().ProviderKeys)
	for k := range ingest {
		if !scanned[k] {
			t.Errorf("ingest key %q not recovered from grain (keys: %v)", k, ids[0].ProviderKeys)
		}
	}
	for k := range scanned {
		if !ingest[k] {
			t.Errorf("grain key %q not produced by ingest", k)
		}
	}
	// A Phase 0 grain shares the base between Work and Instance.
	if ids[0].InstanceID != it.WorkID() || ids[0].WorkID != it.WorkID() {
		t.Errorf("ids = %+v, want both = %q", ids[0], it.WorkID())
	}
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}

func hasIdentifier(ids []codexbf.Identifier, class, value string) bool {
	for _, id := range ids {
		if id.Class == class && id.Value == value {
			return true
		}
	}
	return false
}
