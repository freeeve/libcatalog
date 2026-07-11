package vocab

import (
	"os"
	"slices"
	"testing"

	"github.com/freeeve/libcat/storage/blob"
	"github.com/freeeve/libcat/storage/vocabsidecar"
)

const (
	homoLocal = "https://homosaurus.org/v4/homoitLOCAL"
	homoTrans = "https://homosaurus.org/v4/homoit0001235" // "Transgender people"
)

// mapsFixture loads the shared fixture from the same source path sidecarFixture
// uses, but builds no sidecar artifacts: the wholly map-backed control.
func mapsFixture(t *testing.T) (*Index, blob.Store) {
	t.Helper()
	data, err := os.ReadFile("testdata/authorities.nq")
	if err != nil {
		t.Fatal(err)
	}
	st := blob.NewMem()
	if _, err := st.Put(t.Context(), "data/authorities/vocab/authorities.nq", data, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := Load(t.Context(), st, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return ix, st
}

// cacheTerm plants a loose quad for a scheme, the way vocabsrc.CacheTerm does
// when a cataloger picks a term from a live tab.
func cacheTerm(t *testing.T, ix *Index, st blob.Store, path, nq string) {
	t.Helper()
	if _, err := st.Put(t.Context(), path, []byte(nq), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Reload(t.Context(), st, "data/authorities/", nil); err != nil {
		t.Fatal(err)
	}
}

// tasks/265: one cached live pick used to replay a scheme's whole snapshot
// into resident maps. The scheme must keep its sidecar, and the overlay must
// stay exactly as big as the picks.
func TestOverlayDoesNotMakeTheSnapshotResident(t *testing.T) {
	ix, st := sidecarFixture(t, []string{"homosaurus", "lcsh"})
	before := len(ix.load().schemes["homosaurus"])
	if before != 0 {
		t.Fatalf("an armed scheme starts with %d resident terms, want 0", before)
	}
	cacheTerm(t, ix, st, "data/authorities/cache/homosaurus/a.nq",
		"<"+homoLocal+"> <http://www.w3.org/2004/02/skos/core#prefLabel> \"Locally merged concept\"@en <authority:homosaurus> .\n")

	snap := ix.load()
	if snap.sidecar["homosaurus"] == nil {
		t.Fatal("one cached term dropped the scheme's sidecar")
	}
	if n := len(snap.schemes["homosaurus"]); n != 1 {
		t.Fatalf("resident terms = %d, want 1: the snapshot was replayed into maps", n)
	}
}

// A scheme that carries both a sidecar and an overlay must be named once.
// The admin SPA keys its vocab tabs by scheme name, so a repeat throws
// each_key_duplicate and the picker dialog never renders -- which is what one
// cached live pick did to every sidecar-backed scheme.
func TestSchemesNamesADualBackedSchemeOnce(t *testing.T) {
	ix, st := sidecarFixture(t, []string{"homosaurus", "lcsh"})
	cacheTerm(t, ix, st, "data/authorities/cache/homosaurus/a.nq",
		"<"+homoLocal+"> <http://www.w3.org/2004/02/skos/core#prefLabel> \"Locally merged concept\"@en <authority:homosaurus> .\n")

	snap := ix.load()
	if snap.sidecar["homosaurus"] == nil || len(snap.schemes["homosaurus"]) == 0 {
		t.Fatal("the fixture does not have a scheme in both backends; nothing is under test")
	}
	got := ix.Schemes()
	if !slices.IsSorted(got) {
		t.Fatalf("Schemes() = %v, want sorted", got)
	}
	seen := map[string]bool{}
	for _, s := range got {
		if seen[s] {
			t.Fatalf("Schemes() = %v, names %q twice", got, s)
		}
		seen[s] = true
	}
	if len(got) != 2 {
		t.Fatalf("Schemes() = %v, want exactly homosaurus and lcsh", got)
	}
	// Resolve walks the same union; a duplicate there is wasted work, not a
	// wrong answer, but it must still find the overlay's term.
	if _, ok := ix.Resolve(homoLocal); !ok {
		t.Fatal("Resolve missed the overlay term")
	}
}

// Search must return both backends' hits in one label-ordered run. The
// overlay does not lead by virtue of being the overlay: the merge key is the
// matched label, exactly as a wholly map-backed load would order them.
func TestSearchMergesOverlayAndSidecar(t *testing.T) {
	// The sidecar term matches "Trans" at its altLabel "Trans people", so the
	// merge key to beat is "trans people", not its prefLabel.
	t.Run("overlay label sorts first", func(t *testing.T) {
		ix, st := sidecarFixture(t, []string{"homosaurus", "lcsh"})
		cacheTerm(t, ix, st, "data/authorities/cache/homosaurus/a.nq",
			"<"+homoLocal+"> <http://www.w3.org/2004/02/skos/core#prefLabel> \"Trans archives\"@en <authority:homosaurus> .\n")

		if got := termIDs(ix.Search("homosaurus", "Trans", 10)); len(got) != 2 || got[0] != homoLocal || got[1] != homoTrans {
			t.Fatalf("Search = %v, want the overlay term then the sidecar term", got)
		}
		if one := termIDs(ix.Search("homosaurus", "Trans", 1)); len(one) != 1 || one[0] != homoLocal {
			t.Fatalf("Search with limit 1 = %v, want the label that sorts first", one)
		}
	})

	// "Transputer ..." sorts after it. A pick must not jump the queue just
	// because it lives in the overlay -- that would make result order depend
	// on whether a sidecar happens to be armed.
	t.Run("overlay label sorts second", func(t *testing.T) {
		ix, st := sidecarFixture(t, []string{"homosaurus", "lcsh"})
		cacheTerm(t, ix, st, "data/authorities/cache/homosaurus/a.nq",
			"<"+homoLocal+"> <http://www.w3.org/2004/02/skos/core#prefLabel> \"Transputer zines\"@en <authority:homosaurus> .\n")

		if got := termIDs(ix.Search("homosaurus", "Trans", 10)); len(got) != 2 || got[0] != homoTrans || got[1] != homoLocal {
			t.Fatalf("Search = %v, want the sidecar term then the overlay term", got)
		}
		if one := termIDs(ix.Search("homosaurus", "Trans", 1)); len(one) != 1 || one[0] != homoTrans {
			t.Fatalf("Search with limit 1 = %v, want the label that sorts first", one)
		}
	})
}

// The merged order must be the order a wholly map-backed load of the same
// terms produces -- the parity the sidecar promises, now with an overlay in
// play. This is the property that a "picks lead" merge would silently break.
func TestSearchMergeMatchesAMapBackedLoad(t *testing.T) {
	const looseNQ = "<" + homoLocal + "> <http://www.w3.org/2004/02/skos/core#prefLabel> \"Transputer zines\"@en <authority:homosaurus> .\n"

	side, st := sidecarFixture(t, []string{"homosaurus", "lcsh"})
	cacheTerm(t, side, st, "data/authorities/cache/homosaurus/a.nq", looseNQ)
	if side.load().sidecar["homosaurus"] == nil {
		t.Fatal("fixture lost its sidecar; the merge is not under test")
	}

	// The same two files, loaded with no sidecar artifacts at all.
	plain, pst := mapsFixture(t)
	if _, err := pst.Put(t.Context(), "data/authorities/cache/homosaurus/a.nq", []byte(looseNQ), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := plain.Reload(t.Context(), pst, "data/authorities/", nil); err != nil {
		t.Fatal(err)
	}
	if plain.load().sidecar["homosaurus"] != nil {
		t.Fatal("the control load armed a sidecar")
	}

	for _, q := range []string{"t", "tr", "trans", "transp", "transg", "queer", "zzz"} {
		for _, limit := range []int{1, 2, 10} {
			got, want := termIDs(side.Search("homosaurus", q, limit)), termIDs(plain.Search("homosaurus", q, limit))
			if asJSON(t, got) != asJSON(t, want) {
				t.Errorf("Search(%q, limit=%d): sidecar+overlay = %v, map-backed = %v", q, limit, got, want)
			}
		}
	}
}

// A pick of a term the snapshot already carries lands in both backends. No
// accessor may report it twice -- MatchLabel gates auto-linking, and a
// duplicate suggests the same link twice.
func TestOverlayDuplicateOfASnapshotTermIsDeduped(t *testing.T) {
	ix, st := sidecarFixture(t, []string{"homosaurus", "lcsh"})
	cacheTerm(t, ix, st, "data/authorities/cache/homosaurus/a.nq",
		"<"+homoTrans+"> <http://www.w3.org/2004/02/skos/core#prefLabel> \"Transgender people\"@en <authority:homosaurus> .\n")

	// Subtests, not a straight-line sequence: each accessor dedupes through
	// its own code path, and a t.Fatal in one must not hide the others.
	t.Run("Search", func(t *testing.T) {
		if got := termIDs(ix.Search("homosaurus", "Transgender", 10)); len(got) != 1 {
			t.Fatalf("Search returned the same term %d times: %v", len(got), got)
		}
	})
	t.Run("MatchLabel", func(t *testing.T) {
		if got := ix.MatchLabel("homosaurus", "transgender people"); len(got) != 1 {
			t.Fatalf("MatchLabel returned %d matches for one term", len(got))
		}
	})
	t.Run("Terms", func(t *testing.T) {
		seen := map[string]bool{}
		for _, term := range ix.Terms("homosaurus") {
			if seen[term.ID] {
				t.Fatalf("Terms returned %s twice", term.ID)
			}
			seen[term.ID] = true
		}
	})
}

// When a term is held by both backends, every accessor must resolve it to the
// same record. Lookup has always preferred the overlay -- a cached live pick
// is fresher than the installed snapshot -- so Search, Terms and MatchLabel
// must too, or the picker and the auto-linker disagree about one IRI.
func TestOverlayShadowsTheSnapshotRecordEverywhere(t *testing.T) {
	ix, st := sidecarFixture(t, []string{"homosaurus", "lcsh"})
	cacheTerm(t, ix, st, "data/authorities/cache/homosaurus/a.nq",
		"<"+homoTrans+"> <http://www.w3.org/2004/02/skos/core#prefLabel> \"Transgender people (revised)\"@en <authority:homosaurus> .\n")
	const want = "Transgender people (revised)"

	term, ok := ix.Lookup("homosaurus", homoTrans)
	if !ok || term.Label("en") != want {
		t.Fatalf("Lookup label = %q, want %q", term.Label("en"), want)
	}
	// Both backends match this prefix, and the sidecar's older label sorts
	// first -- the term must still come back once, as the overlay's record.
	got := ix.Search("homosaurus", "Transgender people", 10)
	if len(got) != 1 || got[0].Label("en") != want {
		t.Fatalf("Search returned %d hits labelled %v, want one labelled %q", len(got), termIDs(got), want)
	}
	matches := ix.MatchLabel("homosaurus", want)
	if len(matches) != 1 || matches[0].Term.Label("en") != want {
		t.Fatalf("MatchLabel returned %d matches for the overlay's label", len(matches))
	}
	for _, term := range ix.Terms("homosaurus") {
		if term.ID == homoTrans && term.Label("en") != want {
			t.Fatalf("Terms label = %q, want the overlay's %q", term.Label("en"), want)
		}
	}
}

// A scheme whose own source file is parsed -- because a sibling sharing that
// file could not arm -- is fully resident already, so its sidecar is dropped.
// The test must be "was this scheme's source parsed", not "are there any quads
// for it": the overlay above leaves quads and must demote nothing.
func TestSharedSourceReplayStillDemotesTheSidecar(t *testing.T) {
	ix, st := sidecarFixture(t, []string{"homosaurus", "lcsh"})
	if len(ix.load().sidecar) != 2 {
		t.Fatal("fixture did not arm both schemes")
	}
	// Break one scheme's sidecar so it cannot open; its source is then parsed,
	// which makes the sibling resident too.
	if err := st.Delete(t.Context(), vocabsidecar.Path("data/authorities/", "lcsh", ".uri.rril")); err != nil {
		t.Fatal(err)
	}
	if err := ix.Reload(t.Context(), st, "data/authorities/", nil); err != nil {
		t.Fatal(err)
	}
	snap := ix.load()
	if snap.sidecar["lcsh"] != nil {
		t.Fatal("a scheme with a missing artifact stayed armed")
	}
	if snap.sidecar["homosaurus"] != nil {
		t.Fatal("the sibling kept its sidecar though the shared source was replayed into maps")
	}
	if _, ok := ix.Lookup("homosaurus", homoTrans); !ok {
		t.Fatal("the demoted scheme lost its terms")
	}
	if _, ok := ix.Lookup("lcsh", "http://id.loc.gov/authorities/subjects/sh85118553"); !ok {
		t.Fatal("the unarmed scheme lost its terms")
	}
}
