package project

import (
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"
)

func TestFeedsListsFeedGraphsSorted(t *testing.T) {
	nq := strings.Join([]string{
		`<#wa000001Work> <http://id.loc.gov/ontologies/bibframe/title> "A" <feed:overdrive> .`,
		`<#wb000002Work> <http://id.loc.gov/ontologies/bibframe/title> "B" <feed:marc> .`,
		`<#wc000003Work> <http://id.loc.gov/ontologies/bibframe/title> "C" <feed:marc> .`,
		`<#wd000004Work> <http://id.loc.gov/ontologies/bibframe/title> "D" <lcat:editorial> .`,
		`<#we000005Work> <http://id.loc.gov/ontologies/bibframe/title> "E" .`,
	}, "\n") + "\n"

	got, err := Feeds([]byte(nq))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"marc", "overdrive"}
	if len(got) != len(want) {
		t.Fatalf("Feeds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Feeds = %v, want %v (sorted, deduped)", got, want)
		}
	}
}

// The editorial graph and the default graph are not feeds, and neither is a
// graph whose name merely contains "feed:".
func TestFeedsIgnoresNonFeedGraphs(t *testing.T) {
	nq := `<#wa000001Work> <http://id.loc.gov/ontologies/bibframe/title> "A" <lcat:editorial> .` + "\n" +
		`<#wb000002Work> <http://id.loc.gov/ontologies/bibframe/title> "B" <https://example.org/feed:notreally> .` + "\n"
	got, err := Feeds([]byte(nq))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Feeds = %v, want none", got)
	}
}

// TestPreRenameCount covers the pre-rename-namespace detector (task 497): only
// predicates under the old libcatalog namespace count, current-namespace extras
// and ordinary BIBFRAME statements do not.
func TestPreRenameCount(t *testing.T) {
	nq := strings.Join([]string{
		`<#wa000001Work> <https://github.com/freeeve/libcatalog/ns#extra/cover> "old.jpg" <feed:hardcover> .`,
		`<#wa000001Work> <https://github.com/freeeve/libcatalog/ns#extra/rating> "5" <feed:hardcover> .`,
		`<#wa000001Work> <https://github.com/freeeve/libcat/ns#extra/cover> "new.jpg" <feed:hardcover> .`,
		`<#wa000001Work> <http://id.loc.gov/ontologies/bibframe/title> "A" <feed:hardcover> .`,
	}, "\n") + "\n"
	ds, err := rdf.ParseNQuads([]byte(nq))
	if err != nil {
		t.Fatal(err)
	}
	if n := PreRenameCount(ds); n != 2 {
		t.Fatalf("PreRenameCount = %d, want 2", n)
	}
}

func TestFeedsOnAnEmptyCatalog(t *testing.T) {
	got, err := Feeds(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Feeds = %v, want none", got)
	}
}
