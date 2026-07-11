package vocab

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/freeeve/libcat/storage/blob"
	"github.com/freeeve/libcat/storage/vocabsidecar"
)

// sidecarFixture stores the shared fixture as an installed snapshot, builds
// sidecar artifacts for the requested schemes, and loads an Index over them.
func sidecarFixture(t *testing.T, arm []string) (*Index, blob.Store) {
	t.Helper()
	data, err := os.ReadFile("testdata/authorities.nq")
	if err != nil {
		t.Fatal(err)
	}
	st := blob.NewMem()
	source := "data/authorities/vocab/authorities.nq"
	if _, err := st.Put(t.Context(), source, data, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, scheme := range arm {
		if _, err := BuildSidecar(t.Context(), st, "data/authorities/", scheme, source); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := Load(t.Context(), st, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	return ix, st
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestSidecarParity drives the whole read surface against the map-backed
// and sidecar-backed loads of the same fixture and requires identical
// results -- the sidecar's correctness contract.
func TestSidecarParity(t *testing.T) {
	maps := loadFixture(t, nil)
	side, _ := sidecarFixture(t, []string{"homosaurus", "lcsh"})
	if n := len(side.load().sidecar); n != 2 {
		t.Fatalf("sidecar armed for %d schemes, want 2", n)
	}

	if got, want := asJSON(t, side.Schemes()), asJSON(t, maps.Schemes()); got != want {
		t.Fatalf("Schemes: %s != %s", got, want)
	}
	for _, scheme := range maps.Schemes() {
		mTerms, sTerms := maps.Terms(scheme), side.Terms(scheme)
		if got, want := asJSON(t, sTerms), asJSON(t, mTerms); got != want {
			t.Fatalf("Terms(%s) diverge:\n side=%s\n maps=%s", scheme, got, want)
		}
		for _, mt := range mTerms {
			st, ok := side.Lookup(scheme, mt.ID)
			if !ok {
				t.Fatalf("Lookup(%s, %s) missing in sidecar", scheme, mt.ID)
			}
			if asJSON(t, st) != asJSON(t, mt) {
				t.Fatalf("Lookup(%s, %s) diverges", scheme, mt.ID)
			}
			mr, mok := maps.Resolve(mt.ID)
			sr, sok := side.Resolve(mt.ID)
			if mok != sok || asJSON(t, mr) != asJSON(t, sr) {
				t.Fatalf("Resolve(%s) diverges", mt.ID)
			}
			if got, want := asJSON(t, side.Path(scheme, mt.ID)), asJSON(t, maps.Path(scheme, mt.ID)); got != want {
				t.Fatalf("Path(%s, %s): %s != %s", scheme, mt.ID, got, want)
			}
			for _, id := range append([]string{mt.ID}, append(mt.ExactMatch, mt.CloseMatch...)...) {
				mm, mok := maps.MatchIdentifier(id)
				sm, sok := side.MatchIdentifier(id)
				if mok != sok || asJSON(t, mm) != asJSON(t, sm) {
					t.Fatalf("MatchIdentifier(%s) diverges", id)
				}
			}
			for _, l := range mt.Labels {
				if got, want := asJSON(t, side.MatchLabel(scheme, l)), asJSON(t, maps.MatchLabel(scheme, l)); got != want {
					t.Fatalf("MatchLabel(%s, %q): %s != %s", scheme, l, got, want)
				}
			}
		}
		for _, q := range []string{"t", "tr", "trans", "trans people", "s", "science f", "personas", "zzz", ""} {
			if got, want := asJSON(t, side.Search(scheme, q, 10)), asJSON(t, maps.Search(scheme, q, 10)); got != want {
				t.Fatalf("Search(%s, %q): %s != %s", scheme, q, got, want)
			}
		}
	}
	if _, ok := side.Lookup("homosaurus", "https://homosaurus.org/v4/nope"); ok {
		t.Fatal("unknown term resolved via sidecar")
	}
	if _, ok := side.Lookup("fast", "anything"); ok {
		t.Fatal("unknown scheme resolved via sidecar")
	}
}

// TestSidecarStaleSource edits the snapshot after the artifacts were built:
// the scheme must fall back to maps and serve the edited content.
func TestSidecarStaleSource(t *testing.T) {
	ix, st := sidecarFixture(t, []string{"homosaurus", "lcsh"})
	source := "data/authorities/vocab/authorities.nq"
	data, _, err := st.Get(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	edited := append([]byte(nil), data...)
	edited = append(edited, []byte("\n<http://id.loc.gov/authorities/subjects/shNEW1> <http://www.w3.org/2004/02/skos/core#prefLabel> \"Brand new heading\"@en <authority:lcsh> .\n")...)
	if _, err := st.Put(t.Context(), source, edited, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Reload(t.Context(), st, "data/authorities/", nil); err != nil {
		t.Fatal(err)
	}
	if n := len(ix.load().sidecar); n != 0 {
		t.Fatalf("stale sidecar still armed for %d schemes", n)
	}
	term, ok := ix.Lookup("lcsh", "http://id.loc.gov/authorities/subjects/shNEW1")
	if !ok || term.Labels["en"] != "Brand new heading" {
		t.Fatalf("edited snapshot not served: %+v ok=%v", term, ok)
	}
}

// TestSidecarLooseQuads adds a separate grain carrying quads for an armed
// scheme: the scheme must fall back to maps and index both files' terms.
func TestSidecarLooseQuads(t *testing.T) {
	ix, st := sidecarFixture(t, []string{"homosaurus", "lcsh"})
	grain := "<https://homosaurus.org/v4/homoitLOCAL> <http://www.w3.org/2004/02/skos/core#prefLabel> \"Locally merged concept\"@en <authority:homosaurus> .\n"
	if _, err := st.Put(t.Context(), "data/authorities/aa/grain.nq", []byte(grain), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Reload(t.Context(), st, "data/authorities/", nil); err != nil {
		t.Fatal(err)
	}
	// A loose quad no longer costs the scheme its sidecar. It used to: three
	// accessors read the sidecar alone, so the loader replayed the whole
	// snapshot into maps to keep them correct -- 513k LCSH headings for one
	// cached live pick. The accessors merge now, so the scheme
	// carries a sidecar and a small map overlay together.
	if n := len(ix.load().sidecar); n != 2 {
		t.Fatalf("a loose quad demoted a sidecar-backed scheme: %d armed, want 2", n)
	}
	if snap := ix.load(); len(snap.schemes["homosaurus"]) != 1 {
		t.Fatalf("overlay = %d terms, want just the loose one (the snapshot must not be resident)",
			len(snap.schemes["homosaurus"]))
	}

	// Both backends answer, through every accessor.
	if _, ok := ix.Lookup("homosaurus", "https://homosaurus.org/v4/homoitLOCAL"); !ok {
		t.Fatal("Lookup missed the loose grain term")
	}
	if _, ok := ix.Lookup("homosaurus", "https://homosaurus.org/v4/homoit0001235"); !ok {
		t.Fatal("Lookup missed the snapshot term")
	}
	if got := ix.Search("homosaurus", "Locally merged", 10); len(got) != 1 || got[0].ID != "https://homosaurus.org/v4/homoitLOCAL" {
		t.Fatalf("Search missed the overlay term: %v", termIDs(got))
	}
	if got := ix.Search("homosaurus", "Trans", 10); len(got) == 0 {
		t.Fatal("Search missed the sidecar's terms once an overlay existed")
	}
	if got := ix.MatchLabel("homosaurus", "locally merged concept"); len(got) != 1 {
		t.Fatalf("MatchLabel missed the overlay term: %d matches", len(got))
	}
	all := ix.Terms("homosaurus")
	if len(all) < 2 {
		t.Fatalf("Terms = %d, want the sidecar's terms plus the overlay", len(all))
	}
	seen := map[string]bool{}
	for _, t2 := range all {
		if seen[t2.ID] {
			t.Fatalf("Terms returned %s twice", t2.ID)
		}
		seen[t2.ID] = true
	}
	if !seen["https://homosaurus.org/v4/homoitLOCAL"] || !seen["https://homosaurus.org/v4/homoit0001235"] {
		t.Fatal("Terms did not merge the overlay with the sidecar")
	}
}

func termIDs(ts []*Term) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

// TestSidecarPartialArming builds artifacts for one scheme of a shared
// source file: the file cannot be skipped, so both schemes serve from maps.
func TestSidecarPartialArming(t *testing.T) {
	ix, _ := sidecarFixture(t, []string{"homosaurus"})
	if n := len(ix.load().sidecar); n != 0 {
		t.Fatalf("partially-armed shared source produced %d sidecar schemes", n)
	}
	if got := ix.Schemes(); len(got) != 2 {
		t.Fatalf("schemes = %v", got)
	}
	if _, ok := ix.Lookup("lcsh", "http://id.loc.gov/authorities/subjects/sh85118553"); !ok {
		t.Fatal("lcsh term missing")
	}
	if _, ok := ix.Lookup("homosaurus", "https://homosaurus.org/v4/homoit0001235"); !ok {
		t.Fatal("homosaurus term missing")
	}
}

// TestSidecarSearchIndex covers the RRTI search path through odd inputs
// (multi-byte labels, no-match prefixes) via the public surface.
func TestSidecarSearchIndex(t *testing.T) {
	side, _ := sidecarFixture(t, []string{"homosaurus", "lcsh"})
	got := side.Search("homosaurus", "personas transg", 5)
	if len(got) != 1 || got[0].Labels["es"] != "Personas transgénero" {
		t.Fatalf("multibyte prefix search = %v", asJSON(t, got))
	}
	if hits := side.Search("homosaurus", strings.Repeat("z", 40), 5); len(hits) != 0 {
		t.Fatalf("phantom hits: %v", asJSON(t, hits))
	}
}

// sidecarFiles lists every artifact path currently under the sidecar directory.
func sidecarFiles(t *testing.T, st blob.Store) []string {
	t.Helper()
	var out []string
	for e, err := range st.List(t.Context(), "data/authorities/"+vocabsidecar.DirPart) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e.Path)
	}
	return out
}

// TestRemoveSidecarLeavesNothingBuildSidecarWrote is the drift guard for
// vocabsidecar.Suffixes. It enumerates the blob store instead of trusting that list, so an
// artifact that BuildSidecar starts writing and vocabsidecar.RemoveSidecar forgets fails here
// rather than accumulating on an operator's disk.
func TestRemoveSidecarLeavesNothingBuildSidecarWrote(t *testing.T) {
	_, st := sidecarFixture(t, []string{"lcsh"})
	// Plus the pre-v2 blob a rebuild orphans, which removal must also take.
	legacy := vocabsidecar.Path("data/authorities/", "lcsh", ".search.bin")
	if _, err := st.Put(t.Context(), legacy, []byte("legacy"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	// Control: without this, a removal that deletes nothing passes the check below.
	before := sidecarFiles(t, st)
	if len(before) < 8 {
		t.Fatalf("BuildSidecar wrote only %d artifacts (%v) -- removing them would prove nothing", len(before), before)
	}

	if err := vocabsidecar.RemoveSidecar(t.Context(), st, "data/authorities/", "lcsh"); err != nil {
		t.Fatal(err)
	}
	if after := sidecarFiles(t, st); len(after) != 0 {
		t.Errorf("vocabsidecar.RemoveSidecar left %d of %d artifacts behind: %v", len(after), len(before), after)
	}
	// Removing a scheme twice is as harmless as removing it once: the snapshot is
	// gone by now, and an operator retrying a failed removal must not see an error.
	if err := vocabsidecar.RemoveSidecar(t.Context(), st, "data/authorities/", "lcsh"); err != nil {
		t.Errorf("second removal: %v", err)
	}
}

// TestOrphanSidecarsFindsWhatNoSnapshotBacks: the sweep's detection half. A sidecar
// whose snapshot is present is not an orphan; once the snapshot is gone, it is, and
// removing it leaves the store clean.
func TestOrphanSidecarsFindsWhatNoSnapshotBacks(t *testing.T) {
	_, st := sidecarFixture(t, []string{"homosaurus", "lcsh"})
	ctx := t.Context()

	// Control: with the snapshot present, a live sidecar is not an orphan. Without
	// this the whole test could pass by reporting everything.
	if orphans, err := vocabsidecar.OrphanSidecars(ctx, st, "data/authorities/"); err != nil || len(orphans) != 0 {
		t.Fatalf("live sidecars reported as orphans: %+v err=%v", orphans, err)
	}

	// The snapshot both manifests name is removed -- the pre-v0.137.0 leak.
	if err := st.Delete(ctx, "data/authorities/vocab/authorities.nq"); err != nil {
		t.Fatal(err)
	}
	orphans, err := vocabsidecar.OrphanSidecars(ctx, st, "data/authorities/")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range orphans {
		got[o.Scheme] = o.Reason
	}
	if len(got) != 2 || got["homosaurus"] != vocabsidecar.ReasonSourceMissing || got["lcsh"] != vocabsidecar.ReasonSourceMissing {
		t.Fatalf("orphans = %+v, want homosaurus and lcsh both source-missing", orphans)
	}

	// Sweeping them leaves the store empty and the detection quiet -- the whole point.
	for _, o := range orphans {
		if err := vocabsidecar.RemoveSidecar(ctx, st, "data/authorities/", o.Scheme); err != nil {
			t.Fatal(err)
		}
	}
	if rest := sidecarFiles(t, st); len(rest) != 0 {
		t.Errorf("sweep left %d files: %v", len(rest), rest)
	}
	if again, err := vocabsidecar.OrphanSidecars(ctx, st, "data/authorities/"); err != nil || len(again) != 0 {
		t.Errorf("orphans survive their own sweep: %+v err=%v", again, err)
	}
}

// TestOrphanSidecarsCollectsAnUnreadableManifest: a manifest that no longer parses
// arms nothing, so it is an orphan too. Its scheme comes from the file name, because
// the artifacts are named the same way and can still be collected.
func TestOrphanSidecarsCollectsAnUnreadableManifest(t *testing.T) {
	_, st := sidecarFixture(t, []string{"lcsh"})
	ctx := t.Context()
	bad := vocabsidecar.Path("data/authorities/", "zzbad", vocabsidecar.ManifestSuffix)
	if _, err := st.Put(ctx, bad, []byte("{ not json"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	orphans, err := vocabsidecar.OrphanSidecars(ctx, st, "data/authorities/")
	if err != nil {
		t.Fatal(err)
	}
	var bad1 *vocabsidecar.OrphanSidecar
	for i := range orphans {
		if orphans[i].Scheme == "zzbad" {
			bad1 = &orphans[i]
		}
		// Control: lcsh's snapshot is present, so it must not be swept in with the
		// bad manifest -- an unreadable neighbour is not a reason to condemn a live one.
		if orphans[i].Scheme == "lcsh" {
			t.Errorf("live lcsh reported as an orphan: %+v", orphans[i])
		}
	}
	if bad1 == nil || bad1.Reason != vocabsidecar.ReasonManifestUnreadable {
		t.Fatalf("unreadable manifest not collected as an orphan: %+v", orphans)
	}
}

// TestOrphanSidecarsSpotsAMissingSourceNotATransientError pins the discipline the
// leak's own analysis demanded: only a definitive not-found on the snapshot condemns
// a sidecar. A blob store that errors on the read is not evidence the snapshot is
// gone, so the scan fails rather than reporting a live index as collectable.
func TestOrphanSidecarsSpotsAMissingSourceNotATransientError(t *testing.T) {
	_, st := sidecarFixture(t, []string{"lcsh"})
	ctx := t.Context()
	failing := &getFailsStore{Store: st, on: "data/authorities/vocab/authorities.nq"}
	if _, err := vocabsidecar.OrphanSidecars(ctx, failing, "data/authorities/"); err == nil {
		t.Fatal("a read error on the snapshot was treated as an orphan, not surfaced")
	}
}

// getFailsStore returns a non-not-found error when a specific path is read, so a
// transient failure can be told apart from a genuine absence.
type getFailsStore struct {
	blob.Store
	on string
}

func (s *getFailsStore) Get(ctx context.Context, path string) ([]byte, string, error) {
	if path == s.on {
		return nil, "", errors.New("blob store unavailable")
	}
	return s.Store.Get(ctx, path)
}

// TestRemoveSidecarDoesNotReachIntoANeighbouringScheme pins why removal enumerates
// exact suffixes rather than deleting everything under `sidecar/<scheme>`. A scheme
// is validated only as non-empty (vocabsrc.validateSource), so one scheme's name can
// prefix another's, and prefix-matching the directory would take the neighbour with
// it.
func TestRemoveSidecarDoesNotReachIntoANeighbouringScheme(t *testing.T) {
	_, st := sidecarFixture(t, []string{"lcsh"})
	neighbour := vocabsidecar.Path("data/authorities/", "lcsh.local", vocabsidecar.ManifestSuffix)
	if _, err := st.Put(t.Context(), neighbour, []byte(`{"scheme":"lcsh.local"}`), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := vocabsidecar.RemoveSidecar(t.Context(), st, "data/authorities/", "lcsh"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.Get(t.Context(), neighbour); err != nil {
		t.Fatalf("removing scheme %q took %q with it: %v", "lcsh", neighbour, err)
	}
	// Control: the scheme actually asked for is gone, so the survival above is not
	// simply a removal that did nothing.
	if _, _, err := st.Get(t.Context(), vocabsidecar.Path("data/authorities/", "lcsh", vocabsidecar.ManifestSuffix)); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("lcsh manifest survived its own removal: %v", err)
	}
}
