package similar

import (
	"fmt"
	"slices"
	"testing"
)

// work builds a scorer input; every field the scorer reads is settable.
func work(id string, mod func(*Work)) Work {
	s := Work{WorkID: id}
	if mod != nil {
		mod(&s)
	}
	return s
}

// noBonus isolates the walk from the flat bonuses, so a test that means to
// measure rarity is not reading a language boost.
func noBonus() Options {
	o := DefaultOptions()
	o.LanguageBonus = 0
	o.AvailabilityBonus = 0
	return o
}

// padded returns n filler Works so the DF cap has a catalog to be a fraction of.
func padded(n int) []Work {
	out := make([]Work, 0, n)
	for i := range n {
		out = append(out, work(fmt.Sprintf("pad%03d", i), nil))
	}
	return out
}

func ids(scored []Scored) []string {
	out := make([]string, 0, len(scored))
	for _, s := range scored {
		out = append(out, s.WorkID)
	}
	return out
}

func scoreOf(scored []Scored, id string) float64 {
	for _, s := range scored {
		if s.WorkID == id {
			return s.Score
		}
	}
	return 0
}

// The focus Work is never its own neighbour.
func TestFocusIsExcluded(t *testing.T) {
	works := append(padded(20),
		work("wa", func(s *Work) { s.Subjects = []string{"s:1"} }),
		work("wb", func(s *Work) { s.Subjects = []string{"s:1"} }),
	)
	got := Build(works, noBonus()).Neighbors("wa", 10)

	if slices.Contains(ids(got), "wa") {
		t.Fatalf("the focus work recommended itself: %v", ids(got))
	}
	if ids(got)[0] != "wb" {
		t.Fatalf("neighbors = %v, want wb", ids(got))
	}
}

// Found by running the scorer over the real playground catalog, where a scan
// prefix that also caught catalog.nq yielded four summaries per Work: the focus
// sat at four offsets, Neighbors excluded one, and "Frog and Toad Together" was
// the top recommendation for "Frog and Toad Together".
// A repeated attribute value is evidence once, not twice. Neither caller
// de-duplicates subjects or tags upstream, so a record that states the same
// subject in two graphs would otherwise post to it twice and outscore
// a Work that genuinely shares two distinct subjects.
func TestRepeatedAttributeValueCountsOnce(t *testing.T) {
	works := append(padded(20),
		work("wa", func(s *Work) { s.Subjects = []string{"s:1", "s:1", "s:1"} }),
		work("wdup", func(s *Work) { s.Subjects = []string{"s:1", "s:1", "s:1"} }),
		work("wtwo", func(s *Work) { s.Subjects = []string{"s:1", "s:2"} }),
		work("wother", func(s *Work) { s.Subjects = []string{"s:2"} }),
	)
	got := Build(works, noBonus()).Neighbors("wa", 10)
	if len(got) != 2 {
		t.Fatalf("neighbors = %v, want wdup and wtwo", ids(got))
	}
	// wdup and wtwo each share exactly s:1 with wa. Triple-stating it must not
	// make wdup the better match.
	if got[0].Score != got[1].Score {
		t.Fatalf("a thrice-stated subject outscored a once-stated one: %v", got)
	}
	for _, s := range got {
		if len(s.Shared) != 1 || s.Shared[0] != "s:1" {
			t.Fatalf("shared = %v, want the subject listed once", s.Shared)
		}
	}
}

// Empty attribute values are dropped: an untitled series or a blank language is
// not an attribute two Works can meaningfully share.
func TestEmptyAttributeValuesAreNotSharedAttributes(t *testing.T) {
	works := append(padded(20),
		work("wa", func(s *Work) { s.Series = []string{""} }),
		work("wb", func(s *Work) { s.Series = []string{""} }),
	)
	if got := Build(works, noBonus()).Neighbors("wa", 10); len(got) != 0 {
		t.Fatalf("neighbors = %v, want none: two Works both carrying an empty series are not in a series together", ids(got))
	}
}

// The rail must be identical on every projection of an identical catalog. Two
// sources of drift, both silent: the concept-tree expansion is a map, and Go
// randomizes map iteration; and Shared is truncated to maxShared, so a tie broken
// by insertion order changes *which* explanations the reader sees.
//
// Six equal-weight subjects and a cap of five is the shape that exposes it. Before
// the fix this yielded six different orderings across 200 builds in one process.
func TestSharedIsDeterministicWhenWeightsTie(t *testing.T) {
	subs := []string{"s:aaa", "s:bbb", "s:ccc", "s:ddd", "s:eee", "s:fff"}
	works := append(padded(30),
		work("wa", func(s *Work) { s.Subjects = subs }),
		work("wb", func(s *Work) { s.Subjects = subs }),
	)
	first := Build(works, DefaultOptions()).Neighbors("wa", 5)
	if len(first) != 1 || len(first[0].Shared) != maxShared {
		t.Fatalf("setup: got %d neighbours with %d shared, want 1 with %d", len(first), len(first[0].Shared), maxShared)
	}
	for range 100 {
		got := Build(works, DefaultOptions()).Neighbors("wa", 5)
		if !slices.Equal(got[0].Shared, first[0].Shared) {
			t.Fatalf("Shared reshuffled across builds:\n  %v\n  %v", first[0].Shared, got[0].Shared)
		}
	}
	// Ties break by value, so the survivors are the lexicographically first five.
	if want := subs[:maxShared]; !slices.Equal(first[0].Shared, want) {
		t.Errorf("Shared = %v, want %v (equal weights break by value)", first[0].Shared, want)
	}
}

// The whole ranked result -- ids, scores and explanations -- must reproduce across
// builds of an identical catalog, not just the Shared order above. This is the
// property similar.json's consumers depend on; the pieces are tested separately
// because each can break without the other.
func TestNeighborsAreReproducible(t *testing.T) {
	works := append(padded(30),
		work("wa", func(s *Work) { s.Subjects = []string{"s:1", "s:2", "s:3"}; s.Tags = []string{"t:1"} }),
		work("wb", func(s *Work) { s.Subjects = []string{"s:1", "s:2"}; s.Tags = []string{"t:1"} }),
		work("wc", func(s *Work) { s.Subjects = []string{"s:2", "s:3"} }),
		work("wd", func(s *Work) { s.Subjects = []string{"s:1"}; s.Tags = []string{"t:1"} }),
	)
	first := Build(works, DefaultOptions()).Neighbors("wa", 8)
	if len(first) < 3 {
		t.Fatalf("setup: %d neighbours, want at least 3", len(first))
	}
	for range 50 {
		got := Build(works, DefaultOptions()).Neighbors("wa", 8)
		if len(got) != len(first) {
			t.Fatalf("neighbour count varies: %d vs %d", len(got), len(first))
		}
		for i := range got {
			a, b := first[i], got[i]
			if a.WorkID != b.WorkID || a.Score != b.Score || !slices.Equal(a.Shared, b.Shared) {
				t.Fatalf("neighbour %d varies:\n  %+v\n  %+v", i, a, b)
			}
		}
	}
}

// sharedWith binary-searches the posting lists, which are sorted only because
// Build appends offsets in ascending order. Nothing else enforces that, and if it
// ever stops holding the search silently misses: neighbours keep their scores and
// quietly lose every explanation. Pin the invariant where it is created.
func TestPostingListsAreSortedAscending(t *testing.T) {
	works := append(padded(20),
		work("wa", func(s *Work) { s.Subjects = []string{"s:1"}; s.Tags = []string{"t:1"}; s.Series = []string{"ser"} }),
		work("wb", func(s *Work) { s.Subjects = []string{"s:1"}; s.Tags = []string{"t:1"} }),
		work("wc", func(s *Work) { s.Subjects = []string{"s:1"}; s.Series = []string{"ser"} }),
	)
	ix := Build(works, noBonus())
	for rel := range numRelations {
		for value, posts := range ix.postings[rel] {
			if !slices.IsSorted(posts) {
				t.Errorf("postings[%d][%q] = %v is not ascending; sharedWith's binary search will miss", rel, value, posts)
			}
		}
	}
	// And the explanation actually survives the search.
	got := ix.Neighbors("wa", 5)
	if len(got) == 0 {
		t.Fatal("no neighbours")
	}
	for _, s := range got {
		if len(s.Shared) == 0 {
			t.Errorf("%s scored but explains nothing", s.WorkID)
		}
	}
}

func TestDuplicateSummariesDoNotSelfRecommend(t *testing.T) {
	dup := work("wa", func(s *Work) { s.Subjects = []string{"s:1"}; s.Series = []string{"Frog and Toad"} })
	works := append(padded(20), dup, dup, dup,
		work("wb", func(s *Work) { s.Subjects = []string{"s:1"} }),
	)
	ix := Build(works, noBonus())

	if ix.Len() != 22 {
		t.Fatalf("indexed %d works, want 22: the duplicate WorkID was indexed more than once", ix.Len())
	}
	got := ix.Neighbors("wa", 10)
	if slices.Contains(ids(got), "wa") {
		t.Fatalf("a duplicated work recommended itself: %v", ids(got))
	}
	if len(got) != 1 || got[0].WorkID != "wb" {
		t.Fatalf("neighbors = %v, want only wb", ids(got))
	}
}

// Retiring a record must not leave it recommended from elsewhere,
// and it has no neighbours of its own.
func TestTombstonedWorksAreInvisible(t *testing.T) {
	works := append(padded(20),
		work("wlive", func(s *Work) { s.Subjects = []string{"s:1"} }),
		work("wdead", func(s *Work) { s.Subjects = []string{"s:1"}; s.Tombstoned = true }),
	)
	ix := Build(works, noBonus())

	if got := ix.Neighbors("wlive", 10); len(got) != 0 {
		t.Fatalf("a tombstoned work was recommended: %v", ids(got))
	}
	if got := ix.Neighbors("wdead", 10); got != nil {
		t.Fatalf("a tombstoned work has neighbours: %v", ids(got))
	}
}

// A suppressed Work is hidden from the public, not retired: the admin surface
// shows it, and the projection never sees it upstream. The scorer is blind to
// suppression by construction -- similar.Work has no such field -- so the
// guarantee lives on the converter that builds it. See
// ingest.TestSimilarWorkKeepsSuppressed.

// An attribute nobody else carries links to nothing.
func TestSingletonAttributeContributesNothing(t *testing.T) {
	works := append(padded(20), work("wa", func(s *Work) { s.Subjects = []string{"s:only-mine"} }))

	if got := Build(works, noBonus()).Neighbors("wa", 10); len(got) != 0 {
		t.Fatalf("a singleton subject produced neighbours: %v", ids(got))
	}
}

// The floor counts Works *other than the focus*, so a concept the focus reaches
// only through the tree matches even when exactly one other Work sits under it.
// qllpoc's df >= 2 drops this, because there the focus is not among the carriers
// and df never reaches 2. On a small catalog it is the whole rail.
func TestTreeConceptWithASingleOtherWorkStillMatches(t *testing.T) {
	opts := noBonus()
	opts.Broader = func(iri string) []string {
		if iri == "s:mothers" {
			return []string{"s:parents"}
		}
		return nil
	}
	works := append(padded(20),
		work("focus", func(s *Work) { s.Subjects = []string{"s:mothers"} }),
		work("wonly", func(s *Work) { s.Subjects = []string{"s:parents"} }),
	)

	got := Build(works, opts).Neighbors("focus", 10)
	if len(got) != 1 || got[0].WorkID != "wonly" {
		t.Fatalf("neighbors = %v, want wonly: df=1 on a broader concept means one other work, not none", ids(got))
	}
}

// The same floor still refuses a direct subject only the focus carries: there
// df=1 means nobody else, and there is no one to recommend.
func TestDirectSingletonHasNoOtherWork(t *testing.T) {
	works := append(padded(20),
		work("focus", func(s *Work) { s.Subjects = []string{"s:mine"} }),
		work("wother", func(s *Work) { s.Subjects = []string{"s:theirs"} }),
	)
	if got := Build(works, noBonus()).Neighbors("focus", 10); len(got) != 0 {
		t.Fatalf("neighbors = %v, want none", ids(got))
	}
}

// An attribute on a fifth of the catalog describes the catalog, not the book.
func TestOverCommonAttributeIsCapped(t *testing.T) {
	// 20 works: cap = floor(0.20 * 20) = 4. "s:common" sits on 6 of them.
	var works []Work
	for i := range 20 {
		works = append(works, work(fmt.Sprintf("w%02d", i), func(s *Work) {
			if i < 6 {
				s.Subjects = []string{"s:common"}
			}
		}))
	}
	if got := Build(works, noBonus()).Neighbors("w00", 10); len(got) != 0 {
		t.Fatalf("an over-common subject produced neighbours: %v", ids(got))
	}
}

// The cap must never round away df=2, the single most informative case.
func TestSmallCatalogsStillGetNeighbours(t *testing.T) {
	works := []Work{
		work("wa", func(s *Work) { s.Subjects = []string{"s:1"} }),
		work("wb", func(s *Work) { s.Subjects = []string{"s:1"} }),
		work("wc", nil), work("wd", nil), work("we", nil),
	}
	// floor(0.20 * 5) = 1, which would make df>=2 unreachable.
	if got := Build(works, noBonus()).Neighbors("wa", 10); len(got) != 1 || got[0].WorkID != "wb" {
		t.Fatalf("a five-work catalog yielded %v; the DF cap rounded away df=2", ids(got))
	}
}

// Rarity, the property the whole thing rests on: two books sharing an obscure
// heading have told you more than two sharing a popular one.
func TestRareAttributeOutranksCommonOne(t *testing.T) {
	// 40 works => cap 8. s:rare on 2, s:mid on 7. Focus carries both.
	var works []Work
	for i := range 40 {
		works = append(works, work(fmt.Sprintf("p%02d", i), nil))
	}
	works = append(works,
		work("focus", func(s *Work) { s.Subjects = []string{"s:rare", "s:mid"} }),
		work("wrare", func(s *Work) { s.Subjects = []string{"s:rare"} }),
	)
	for i := range 6 {
		works = append(works, work(fmt.Sprintf("wmid%d", i), func(s *Work) { s.Subjects = []string{"s:mid"} }))
	}

	got := Build(works, noBonus()).Neighbors("focus", 10)
	if got[0].WorkID != "wrare" {
		t.Fatalf("neighbors = %v, want the rare-subject share first", ids(got))
	}
	if scoreOf(got, "wrare") <= scoreOf(got, "wmid0") {
		t.Fatalf("rare %.4f <= mid %.4f: idf is not weighting", scoreOf(got, "wrare"), scoreOf(got, "wmid0"))
	}
}

// A series is stronger evidence than a subject, which is what the weights say.
func TestSeriesOutweighsSubject(t *testing.T) {
	works := append(padded(20),
		work("focus", func(s *Work) { s.Series = []string{"Locked Tomb"}; s.Subjects = []string{"s:1"} }),
		work("wseries", func(s *Work) { s.Series = []string{"Locked Tomb"} }),
		work("wsubject", func(s *Work) { s.Subjects = []string{"s:1"} }),
	)
	got := Build(works, noBonus()).Neighbors("focus", 10)

	if got[0].WorkID != "wseries" {
		t.Fatalf("neighbors = %v, want the series-mate first", ids(got))
	}
}

// The tree walk is the point: two Works whose subjects are siblings under one
// broader concept are neighbours even though no Work carries both IRIs.
func TestSiblingSubjectsMatchThroughTheTree(t *testing.T) {
	broader := map[string][]string{"s:mothers": {"s:parents"}, "s:fathers": {"s:parents"}}
	opts := noBonus()
	opts.Broader = func(iri string) []string { return broader[iri] }

	works := append(padded(20),
		work("focus", func(s *Work) { s.Subjects = []string{"s:mothers"} }),
		work("wsibling", func(s *Work) { s.Subjects = []string{"s:parents"} }),
		work("wparent", func(s *Work) { s.Subjects = []string{"s:parents"} }),
	)

	got := Build(works, opts).Neighbors("focus", 10)
	if !slices.Contains(ids(got), "wsibling") {
		t.Fatalf("neighbors = %v; the broader walk found nothing", ids(got))
	}

	// Without the hook, the same catalog yields nothing: the tree is doing it.
	if bare := Build(works, noBonus()).Neighbors("focus", 10); len(bare) != 0 {
		t.Fatalf("no-tree neighbors = %v, want none", ids(bare))
	}
}

// A broader hop is worth less than the direct hit.
func TestTreeHopsDecay(t *testing.T) {
	opts := noBonus()
	opts.Broader = func(iri string) []string {
		if iri == "s:child" {
			return []string{"s:parent"}
		}
		return nil
	}
	works := append(padded(20),
		work("focus", func(s *Work) { s.Subjects = []string{"s:child"} }),
		work("wdirect", func(s *Work) { s.Subjects = []string{"s:child"} }),
		work("wbroader", func(s *Work) { s.Subjects = []string{"s:parent"} }),
		work("wbroader2", func(s *Work) { s.Subjects = []string{"s:parent"} }),
	)

	got := Build(works, opts).Neighbors("focus", 10)
	if scoreOf(got, "wdirect") <= scoreOf(got, "wbroader") {
		t.Fatalf("direct %.4f <= broader %.4f: the hop did not decay", scoreOf(got, "wdirect"), scoreOf(got, "wbroader"))
	}
}

// A diamond in the hierarchy contributes once, at its best weight, rather than
// compounding into a spuriously strong match.
func TestDiamondHierarchyDoesNotCompound(t *testing.T) {
	// child -> {momA, momB} -> gran
	opts := noBonus()
	opts.Broader = func(iri string) []string {
		switch iri {
		case "s:child":
			return []string{"s:momA", "s:momB"}
		case "s:momA", "s:momB":
			return []string{"s:gran"}
		}
		return nil
	}
	w := Build(append(padded(20),
		work("focus", func(s *Work) { s.Subjects = []string{"s:child"} }),
		work("wgran", func(s *Work) { s.Subjects = []string{"s:gran"} }),
		work("wgran2", func(s *Work) { s.Subjects = []string{"s:gran"} }),
	), opts)

	// gran is reached twice at hop 2. Its weight must be TreeDecay^2, once.
	want := DefaultWeights[RelSubject] * 0.25 * idf(2)
	if got := scoreOf(w.Neighbors("focus", 10), "wgran"); got != want {
		t.Fatalf("gran score = %.6f, want %.6f: the diamond compounded", got, want)
	}
}

// Language cannot put a Work on the list; it only reorders Works already there.
// Every book shares a language with most of the catalog.
func TestLanguageIsABonusNotAnEdge(t *testing.T) {
	opts := DefaultOptions()
	works := append(padded(20),
		work("focus", func(s *Work) { s.Languages = []string{"en"}; s.Subjects = []string{"s:1"} }),
		work("wlangonly", func(s *Work) { s.Languages = []string{"en"} }),
		work("wsubject", func(s *Work) { s.Languages = []string{"es"}; s.Subjects = []string{"s:1"} }),
	)

	got := Build(works, opts).Neighbors("focus", 10)
	if slices.Contains(ids(got), "wlangonly") {
		t.Fatalf("a shared language alone put a work on the rail: %v", ids(got))
	}
	if len(got) != 1 || got[0].WorkID != "wsubject" {
		t.Fatalf("neighbors = %v, want only the subject match", ids(got))
	}
}

// Two candidates tied on the walk: the one in the reader's language wins.
func TestLanguageBonusBreaksTowardTheReadersLanguage(t *testing.T) {
	opts := DefaultOptions()
	opts.AvailabilityBonus = 0
	works := append(padded(20),
		work("focus", func(s *Work) { s.Languages = []string{"en"}; s.Subjects = []string{"s:1"} }),
		work("wen", func(s *Work) { s.Languages = []string{"en"}; s.Subjects = []string{"s:1"} }),
		work("wes", func(s *Work) { s.Languages = []string{"es"}; s.Subjects = []string{"s:1"} }),
	)

	got := Build(works, opts).Neighbors("focus", 10)
	if got[0].WorkID != "wen" {
		t.Fatalf("neighbors = %v, want the same-language work first", ids(got))
	}
	if d := scoreOf(got, "wen") - scoreOf(got, "wes"); d != opts.LanguageBonus {
		t.Fatalf("language gap = %.4f, want exactly %.4f", d, opts.LanguageBonus)
	}
}

// The same catalog must always yield the same rail; a build step that reshuffled
// its output would churn every OPAC page for nothing.
func TestRankingIsDeterministic(t *testing.T) {
	works := append(padded(20),
		work("focus", func(s *Work) { s.Subjects = []string{"s:1"} }),
		work("wb", func(s *Work) { s.Subjects = []string{"s:1"} }),
		work("wa", func(s *Work) { s.Subjects = []string{"s:1"} }),
		work("wc", func(s *Work) { s.Subjects = []string{"s:1"} }),
	)
	ix := Build(works, noBonus())

	first := ids(ix.Neighbors("focus", 10))
	if want := []string{"wa", "wb", "wc"}; !slices.Equal(first, want) {
		t.Fatalf("tied neighbours = %v, want %v (ties break by work id)", first, want)
	}
	for range 20 {
		if got := ids(ix.Neighbors("focus", 10)); !slices.Equal(got, first) {
			t.Fatalf("ranking is not deterministic: %v then %v", first, got)
		}
	}
}

// "Why is this here?" must have an answer.
func TestSharedAttributesExplainTheMatch(t *testing.T) {
	works := append(padded(20),
		work("focus", func(s *Work) { s.Series = []string{"Locked Tomb"}; s.Subjects = []string{"s:1"} }),
		work("wb", func(s *Work) { s.Series = []string{"Locked Tomb"}; s.Subjects = []string{"s:1"} }),
	)
	got := Build(works, noBonus()).Neighbors("focus", 10)

	// Most valuable first: the series, then the subject.
	if want := []string{"Locked Tomb", "s:1"}; !slices.Equal(got[0].Shared, want) {
		t.Fatalf("shared = %v, want %v", got[0].Shared, want)
	}
}

func TestNeighborsRespectsTheLimit(t *testing.T) {
	works := padded(40)
	for i := range 10 {
		works = append(works, work(fmt.Sprintf("w%d", i), func(s *Work) { s.Subjects = []string{"s:1"} }))
	}
	ix := Build(works, noBonus())

	if got := ix.Neighbors("w0", 3); len(got) != 3 {
		t.Fatalf("limit 3 returned %d", len(got))
	}
	if got := ix.Neighbors("w0", 0); got != nil {
		t.Fatalf("limit 0 returned %v", ids(got))
	}
	if got := ix.Neighbors("nosuchwork", 5); got != nil {
		t.Fatalf("unknown work returned %v", ids(got))
	}
}
