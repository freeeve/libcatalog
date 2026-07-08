package project

import (
	"testing"

	"github.com/freeeve/libcat/bibframe"
)

// visibilityCorpus: two Works; helpers then tombstone/suppress w1.
func visibilityCorpus(t *testing.T) []byte {
	t.Helper()
	const nq = `<#w1Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/title> _:t1 <feed:overdrive> .
_:t1 <http://id.loc.gov/ontologies/bibframe/mainTitle> "First" <feed:overdrive> .
<#w2Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#w2Work> <http://id.loc.gov/ontologies/bibframe/title> _:t2 <feed:overdrive> .
_:t2 <http://id.loc.gov/ontologies/bibframe/mainTitle> "Second" <feed:overdrive> .
`
	return []byte(nq)
}

func TestTombstoneProjection(t *testing.T) {
	corpus := visibilityCorpus(t)
	// Tombstone w1 with w2 as successor.
	updated, err := bibframe.SetTombstone(corpus, "w1", "w2")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := Project(updated, "overdrive")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Works) != 1 || cat.Works[0].ID != "w2" {
		t.Fatalf("works after tombstone = %+v", cat.Works)
	}
	rm, err := Redirects(updated)
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Redirects) != 1 || rm.Redirects[0].From != "w1" || rm.Redirects[0].To != "w2" {
		t.Fatalf("redirects = %+v", rm.Redirects)
	}

	// A tombstone with no successor leaves an empty-target entry.
	noTarget, err := bibframe.SetTombstone(corpus, "w1", "")
	if err != nil {
		t.Fatal(err)
	}
	rm, err = Redirects(noTarget)
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Redirects) != 1 || rm.Redirects[0].From != "w1" || rm.Redirects[0].To != "" {
		t.Fatalf("gone redirects = %+v", rm.Redirects)
	}

	// Restoring brings the work back with no redirect.
	restored, err := bibframe.ClearTombstone(updated, "w1")
	if err != nil {
		t.Fatal(err)
	}
	cat, _ = Project(restored, "overdrive")
	if len(cat.Works) != 2 {
		t.Fatalf("works after restore = %+v", cat.Works)
	}
	rm, _ = Redirects(restored)
	if len(rm.Redirects) != 0 {
		t.Fatalf("redirects after restore = %+v", rm.Redirects)
	}
}

func TestSuppressProjection(t *testing.T) {
	corpus := visibilityCorpus(t)
	updated, err := bibframe.SetSuppressed(corpus, "w1", true)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := Project(updated, "overdrive")
	if err != nil {
		t.Fatal(err)
	}
	if len(cat.Works) != 1 || cat.Works[0].ID != "w2" {
		t.Fatalf("works while suppressed = %+v", cat.Works)
	}
	// Suppression never redirects.
	rm, err := Redirects(updated)
	if err != nil {
		t.Fatal(err)
	}
	if len(rm.Redirects) != 0 {
		t.Fatalf("suppress redirected: %+v", rm.Redirects)
	}
	// The stance reads back and clears.
	v, err := bibframe.Visibility(updated, "w1")
	if err != nil || !v.Suppressed || v.Tombstoned {
		t.Fatalf("visibility = %+v, %v", v, err)
	}
	restored, err := bibframe.SetSuppressed(updated, "w1", false)
	if err != nil {
		t.Fatal(err)
	}
	cat, _ = Project(restored, "overdrive")
	if len(cat.Works) != 2 {
		t.Fatalf("works after unsuppress = %+v", cat.Works)
	}
}
