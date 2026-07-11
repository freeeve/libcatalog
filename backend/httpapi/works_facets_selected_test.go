// the rail is the only account of what the query is. A selected facet
// value that falls out of its group's top-N -- or whose count under the other
// groups' filters is zero -- vanished from the response while staying in the
// query, so the user could not uncheck a filter they could not see.
package httpapi

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/freeeve/libcat/ingest"
)

// tagLadder builds works so that tag "t00" is carried by the most works and
// "tNN" by the fewest, giving the tag group a strict count ranking deeper than
// the top-N cap.
func tagLadder(n int) []ingest.WorkSummary {
	var works []ingest.WorkSummary
	for i := 0; i < n; i++ {
		tag := fmt.Sprintf("t%02d", i)
		for j := 0; j <= n-i; j++ {
			works = append(works, sum(func(s *ingest.WorkSummary) {
				s.WorkID = fmt.Sprintf("w-%s-%d", tag, j)
				s.Tags = []string{tag}
			}))
		}
	}
	return works
}

// countFacets runs one request's groups over the works and returns the response.
func countFacets(groups []facetGroup, works []ingest.WorkSummary) map[string][]facetCount {
	c := newFacetCounter(groups)
	for _, s := range works {
		c.add(s, groupMatches(groups, s))
	}
	return c.result()
}

func findFacet(list []facetCount, value string) (facetCount, bool) {
	for _, f := range list {
		if f.Value == value {
			return f, true
		}
	}
	return facetCount{}, false
}

// The report's deep-link case: ?tag=poetry, where poetry ranks below the top 20.
func TestSelectedTagSurvivesTheTopNCut(t *testing.T) {
	works := tagLadder(facetTopN + 5)
	rare := fmt.Sprintf("t%02d", facetTopN+3) // ranked below the cut

	groups := workFacetGroups(url.Values{"tag": {rare}}, nil, nil)
	tags := countFacets(groups, works)["tag"]

	got, ok := findFacet(tags, rare)
	if !ok {
		t.Fatalf("selected tag %q is absent from its own facet group (%d values): the user cannot uncheck what they cannot see", rare, len(tags))
	}
	if want := 3; got.Count != want { // the ladder gives t23 exactly three works
		t.Fatalf("selected tag %q count = %d, want %d: a pinned value must carry its true count", rare, got.Count, want)
	}
}

// A selected value whose count is zero under the other groups' filters never
// enters the counter at all -- absent rather than merely truncated.
func TestSelectedValueWithNoMatchesIsStillShown(t *testing.T) {
	works := []ingest.WorkSummary{
		sum(func(s *ingest.WorkSummary) { s.WorkID = "w1"; s.Tags = []string{"poetry"}; s.Items = 1 }),
		sum(func(s *ingest.WorkSummary) { s.WorkID = "w2"; s.Tags = []string{"fiction"}; s.Items = 0 }),
	}
	// no-holdings works carry no "poetry" tag, so poetry's count here is 0.
	groups := workFacetGroups(url.Values{"tag": {"poetry"}, "holdings": {"none"}}, nil, nil)
	tags := countFacets(groups, works)["tag"]

	got, ok := findFacet(tags, "poetry")
	if !ok {
		t.Fatalf("selected tag with no matches vanished from the rail; values = %v", tags)
	}
	if got.Count != 0 {
		t.Fatalf("poetry count = %d, want 0: the honest answer, not a hidden filter", got.Count)
	}
}

// Tag matching folds case, and the counter keys on the folded value. A selection
// typed in any case must pin the same bucket, once.
func TestSelectedTagIsPinnedCaseInsensitively(t *testing.T) {
	works := tagLadder(facetTopN + 5)

	groups := workFacetGroups(url.Values{"tag": {"T23", "t23"}}, nil, nil)
	tags := countFacets(groups, works)["tag"]

	var seen []facetCount
	for _, f := range tags {
		if strings.EqualFold(f.Value, "t23") {
			seen = append(seen, f)
		}
	}
	// One row, keyed the way the counter keys it. An unfolded "T23" would be a
	// second checkbox for the same filter, carrying a count of zero.
	if len(seen) != 1 {
		t.Fatalf("folded selection produced %d rows for t23, want exactly 1: %v", len(seen), seen)
	}
	if seen[0].Value != "t23" || seen[0].Count != 3 {
		t.Fatalf("pinned row = %+v, want {t23 3}: the value must match the counter's key", seen[0])
	}
}

// Pinning must not open the cap for everything else: the rail stays bounded.
func TestPinnedSelectionDoesNotUncapTheGroup(t *testing.T) {
	works := tagLadder(facetTopN + 5)
	rare := fmt.Sprintf("t%02d", facetTopN+3)

	groups := workFacetGroups(url.Values{"tag": {rare}}, nil, nil)
	tags := countFacets(groups, works)["tag"]

	if len(tags) != facetTopN+1 {
		t.Fatalf("tag group returned %d values, want %d (the cap plus the one pinned selection)", len(tags), facetTopN+1)
	}
}

// Subjects cap per scheme and carry a scheme annotation; a pinned subject needs
// both, or the rail cannot decide which vocabulary group to render it under.
func TestSelectedSubjectSurvivesThePerSchemeCut(t *testing.T) {
	var works []ingest.WorkSummary
	for i := 0; i < facetTopN+5; i++ {
		iri := fmt.Sprintf("h:%03d", i)
		for j := 0; j <= facetTopN+5-i; j++ {
			works = append(works, sum(func(s *ingest.WorkSummary) {
				s.WorkID = fmt.Sprintf("w-%s-%d", iri, j)
				s.Subjects = []string{iri}
			}))
		}
	}
	schemeOf := func(iri string) string {
		if strings.HasPrefix(iri, "h:") {
			return "homosaurus"
		}
		return "fast"
	}
	rare := fmt.Sprintf("h:%03d", facetTopN+3)

	groups := workFacetGroups(url.Values{"subject": {rare}}, nil, schemeOf)
	subjects := countFacets(groups, works)["subject"]

	got, ok := findFacet(subjects, rare)
	if !ok {
		t.Fatalf("selected subject %q fell out of its scheme's top-%d and vanished", rare, facetTopN)
	}
	if got.Scheme != "homosaurus" {
		t.Fatalf("pinned subject scheme = %q, want homosaurus: an unannotated value has no rail group", got.Scheme)
	}
}

// The response must be a function of the request, not of Go's map iteration
// order. Run this with -count=20 and it holds; drop the sort and it does not.
func TestPinnedSelectionsAreOrderedDeterministically(t *testing.T) {
	works := []ingest.WorkSummary{
		sum(func(s *ingest.WorkSummary) { s.WorkID = "w1"; s.Tags = []string{"kept"}; s.Items = 0 }),
	}
	// None of these three tags exists, so all three are appended selections.
	groups := workFacetGroups(url.Values{"tag": {"zeta", "alpha", "mu"}}, nil, nil)

	var tail []string
	for _, f := range countFacets(groups, works)["tag"] {
		if f.Value != "kept" {
			tail = append(tail, f.Value)
		}
	}
	if want := []string{"alpha", "mu", "zeta"}; !equalStrings(tail, want) {
		t.Fatalf("appended selections = %v, want %v in sorted order", tail, want)
	}
}

// An extras group (e.g. sources) is capped like tags and pins alike.
func TestSelectedExtraSurvivesTheTopNCut(t *testing.T) {
	var works []ingest.WorkSummary
	for i := 0; i < facetTopN+5; i++ {
		src := fmt.Sprintf("s%02d", i)
		for j := 0; j <= facetTopN+5-i; j++ {
			works = append(works, sum(func(s *ingest.WorkSummary) {
				s.WorkID = fmt.Sprintf("w-%s-%d", src, j)
				s.Extras = map[string]string{"sources": src}
			}))
		}
	}
	rare := fmt.Sprintf("s%02d", facetTopN+3)

	groups := workFacetGroups(url.Values{"sources": {rare}}, []string{"sources"}, nil)
	got, ok := findFacet(countFacets(groups, works)["sources"], rare)
	if !ok {
		t.Fatal("selected extras value vanished from its group")
	}
	if got.Count == 0 {
		t.Fatalf("pinned extras value %q count = 0, want its true count", rare)
	}
}
