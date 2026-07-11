// "Export these results…" sat beside a count of 465 and resolved to
// the entire 62,602-work catalog, because no selection could express a facet.
// A selection now narrows by the same dimensions the works rail offers, using
// the same bucketing rules (ingest/facets.go), so the count beside the button
// and the count in the file cannot disagree.
package batch_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/batch"
)

func resolvedIDs(t *testing.T, svc *batch.Service, sel batch.Selection) []string {
	t.Helper()
	targets, err := svc.Resolve(t.Context(), sel, "lib@example.org")
	if err != nil {
		t.Fatalf("resolve %+v: %v", sel, err)
	}
	out := make([]string, 0, len(targets))
	for _, tg := range targets {
		out = append(out, tg.WorkID)
	}
	slices.Sort(out)
	return out
}

// retire tombstones a seeded work the way POST /v1/works/{id}/visibility does.
func retire(t *testing.T, st blob.Store, workID string) {
	t.Helper()
	grain, etag, err := st.Get(t.Context(), bibframe.GrainPath(workID))
	if err != nil {
		t.Fatal(err)
	}
	dead, err := bibframe.SetTombstone(grain, workID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(t.Context(), bibframe.GrainPath(workID), dead, blob.PutOptions{IfMatch: etag}); err != nil {
		t.Fatal(err)
	}
}

// The headline: the whole catalog, narrowed by a tag, is a selection now.
func TestKindAllNarrowsByFacets(t *testing.T) {
	svc, _, _, _ := newService(t)

	all := resolvedIDs(t, svc, batch.Selection{Kind: batch.KindAll})
	if len(all) != 3 {
		t.Fatalf("kind=all resolved %v, want 3 works", all)
	}
	got := resolvedIDs(t, svc, batch.Selection{
		Kind:   batch.KindAll,
		Facets: map[string][]string{"tag": {"space opera"}},
	})
	if want := []string{"wbatch0000001", "wbatch0000002"}; !slices.Equal(got, want) {
		t.Fatalf("kind=all + tag facet = %v, want %v", got, want)
	}
}

// A query and a facet narrow together (AND), which is what the rail draws.
func TestKindSearchAndsWithFacets(t *testing.T) {
	svc, _, _, _ := newService(t)

	got := resolvedIDs(t, svc, batch.Selection{
		Kind:   batch.KindSearch,
		Query:  "ninth",
		Facets: map[string][]string{"tag": {"cozy quest"}},
	})
	if len(got) != 0 {
		t.Fatalf("search 'ninth' AND tag 'cozy quest' = %v, want none", got)
	}
	got = resolvedIDs(t, svc, batch.Selection{
		Kind:   batch.KindSearch,
		Query:  "ninth",
		Facets: map[string][]string{"tag": {"space opera"}},
	})
	if want := []string{"wbatch0000001", "wbatch0000002"}; !slices.Equal(got, want) {
		t.Fatalf("search + facet = %v, want %v", got, want)
	}
}

// Values within one group OR together.
func TestFacetValuesWithinAGroupOr(t *testing.T) {
	svc, _, _, _ := newService(t)

	got := resolvedIDs(t, svc, batch.Selection{
		Kind:   batch.KindAll,
		Facets: map[string][]string{"tag": {"space opera", "cozy quest"}},
	})
	if len(got) != 3 {
		t.Fatalf("two tag values = %v, want all 3 works", got)
	}
}

// Groups AND across each other.
func TestFacetGroupsAndAcross(t *testing.T) {
	svc, _, _, _ := newService(t)

	// Every seeded work has no items and no availability, so holdings=none
	// passes all three; the tag then narrows to two.
	got := resolvedIDs(t, svc, batch.Selection{
		Kind:   batch.KindAll,
		Facets: map[string][]string{"holdings": {"none"}, "tag": {"space opera"}},
	})
	if want := []string{"wbatch0000001", "wbatch0000002"}; !slices.Equal(got, want) {
		t.Fatalf("holdings AND tag = %v, want %v", got, want)
	}
	// A group that matches nothing empties the selection, rather than being
	// ignored the way the old href silently ignored every facet.
	got = resolvedIDs(t, svc, batch.Selection{
		Kind:   batch.KindAll,
		Facets: map[string][]string{"holdings": {"physical"}, "tag": {"space opera"}},
	})
	if len(got) != 0 {
		t.Fatalf("holdings=physical AND tag = %v, want none", got)
	}
}

// Tags are free text a cataloger typed, so they match case-insensitively --
// the same rule the rail uses, or a link copied from the rail would resolve
// differently than the rail counted.
func TestTagFacetFoldsCase(t *testing.T) {
	svc, _, _, _ := newService(t)

	got := resolvedIDs(t, svc, batch.Selection{
		Kind:   batch.KindAll,
		Facets: map[string][]string{"tag": {"SPACE Opera"}},
	})
	if len(got) != 2 {
		t.Fatalf("folded tag = %v, want 2 works", got)
	}
}

// "Entire catalog" has always meant the entire catalog. A selection defaults to
// including retired records so nobody's existing export silently shrinks.
func TestSelectionIncludesTombstonedByDefault(t *testing.T) {
	svc, st, _, _ := newService(t)
	retire(t, st, "wbatch0000003")

	if got := resolvedIDs(t, svc, batch.Selection{Kind: batch.KindAll}); len(got) != 3 {
		t.Fatalf("kind=all = %v, want all 3 including the retired one", got)
	}
}

// The works screen sends exclude explicitly, so its count and the export's agree.
func TestSelectionCanExcludeOrIsolateTombstoned(t *testing.T) {
	svc, st, _, _ := newService(t)
	retire(t, st, "wbatch0000003")

	got := resolvedIDs(t, svc, batch.Selection{Kind: batch.KindAll, Tombstoned: "exclude"})
	if want := []string{"wbatch0000001", "wbatch0000002"}; !slices.Equal(got, want) {
		t.Fatalf("tombstoned=exclude = %v, want %v", got, want)
	}
	got = resolvedIDs(t, svc, batch.Selection{Kind: batch.KindAll, Tombstoned: "only"})
	if want := []string{"wbatch0000003"}; !slices.Equal(got, want) {
		t.Fatalf("tombstoned=only = %v, want %v", got, want)
	}
}

func TestSelectionRejectsAnUnknownTombstonedMode(t *testing.T) {
	svc, _, _, _ := newService(t)

	_, err := svc.Resolve(t.Context(), batch.Selection{Kind: batch.KindAll, Tombstoned: "yes"}, "lib@example.org")
	if !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation: a misspelled mode must not silently include everything", err)
	}
}

// the invariant, which facets must not erode: KindAll is the only way to
// say "everything". A whitespace query plus a facet is still not a search.
func TestFacetsDoNotRescueAnEmptySearchQuery(t *testing.T) {
	svc, _, _, _ := newService(t)

	_, err := svc.Resolve(t.Context(), batch.Selection{
		Kind:   batch.KindSearch,
		Query:  "   ",
		Facets: map[string][]string{"tag": {"space opera"}},
	}, "lib@example.org")
	if !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("err = %v, want ErrValidation: 'everything, filtered' is kind=all + facets", err)
	}
}

// An unrecognized group name is an extras key, not an error -- a deployment's
// extras dimensions are configured, and an unknown one simply matches nothing.
func TestUnknownFacetGroupIsAnExtrasKeyAndMatchesNothing(t *testing.T) {
	svc, _, _, _ := newService(t)

	got := resolvedIDs(t, svc, batch.Selection{
		Kind:   batch.KindAll,
		Facets: map[string][]string{"sources": {"mombian"}},
	})
	if len(got) != 0 {
		t.Fatalf("unknown extras facet = %v, want none: it must narrow, not be ignored", got)
	}
}
