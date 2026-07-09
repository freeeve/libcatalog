package project

import (
	"strings"
	"testing"
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

func TestFeedsOnAnEmptyCatalog(t *testing.T) {
	got, err := Feeds(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Feeds = %v, want none", got)
	}
}
