// the published catalog.nq.gz changed its sha256 on every release even
// when the catalog had not. Blank-node labels were assigned by a running counter
// over the whole traversal, so any change to the serializer's own traversal order
// renamed every node. Labels now come from the grain, namespaced by work id.
package export

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"
)

// nqOf exports work ids from one already-built grain store. It deliberately does
// not rebuild the fixture: ingest stamps timestamps into adminMetadata, so a second
// ingest is a *different corpus*, and comparing across it would test nothing.
func nqOf(t *testing.T, svc *Service, workIDs []string) []byte {
	t.Helper()
	job, err := svc.Create(t.Context(), "lib@example.org", FormatNQuads, Selection{WorkIDs: workIDs})
	if err != nil {
		t.Fatal(err)
	}
	out, err := svc.Open(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// The dump must be a pure function of the grains it contains. This is the property
// the reporter needs: an unchanged catalog exports byte-identically, so the
// manifest's sha256 moves only when the data does.
func TestNQuadsExportIsByteStableAcrossRuns(t *testing.T) {
	bs, all := buildFixtureTree(t)
	svc := newService(t, bs)
	a, b := nqOf(t, svc, all), nqOf(t, svc, all)
	if !bytes.Equal(a, b) {
		t.Fatalf("two exports of the same corpus differ (%d vs %d bytes)", len(a), len(b))
	}
	if len(a) == 0 {
		t.Fatal("empty export; the fixture stopped exercising anything")
	}
}

// Blank-node labels carry the grain's id, not a document-wide counter. `_:b12`
// tells you how many blank nodes the traversal had already seen; `_:wabc_c14n3`
// tells you which node of which grain it is, and cannot move when a neighbouring
// grain changes.
func TestNQuadsExportLabelsAreNamespacedByGrain(t *testing.T) {
	bs, all := buildFixtureTree(t)
	out := nqOf(t, newService(t, bs), all)
	if seq := regexp.MustCompile(`(^|\s)_:b\d+(\s|\.)`).FindIndex(out); seq != nil {
		t.Errorf("export still emits traversal-counter labels: %q", out[seq[0]:min(seq[1]+40, len(out))])
	}
	ds, err := rdf.ParseNQuads(out)
	if err != nil {
		t.Fatalf("export does not parse: %v", err)
	}
	// Every label is `<grainId>_<the grain's own label>`, so the prefix identifies
	// the grain. A shared prefix would mean two grains' `_:c14n0` had merged.
	prefixes := map[string]bool{}
	blanks := 0
	for _, q := range ds.Quads {
		for _, term := range []rdf.Term{q.S, q.O, q.G} {
			if !term.IsBlank() {
				continue
			}
			blanks++
			id, label, ok := strings.Cut(term.Value, "_")
			if !ok || label == "" {
				t.Fatalf("blank label %q is not namespaced by a grain id", term.Value)
			}
			prefixes[id] = true
		}
	}
	if blanks == 0 {
		t.Fatal("fixture carries no blank nodes, so this test proves nothing")
	}
	for _, id := range all {
		if !prefixes[id] {
			t.Errorf("no blank node is namespaced by work %q; prefixes seen: %v", id, prefixes)
		}
	}
	if len(prefixes) < 2 {
		t.Fatalf("all %d blank nodes share one prefix %v -- grains are not namespaced apart", blanks, prefixes)
	}
}

// A grain contributes the same lines whether it is exported alone or with the rest
// of the corpus. Under the old scheme, exporting a subset renumbered everything.
func TestNQuadsExportOfOneWorkMatchesItsLinesInTheFullDump(t *testing.T) {
	bs, all := buildFixtureTree(t)
	if len(all) < 2 {
		t.Skip("fixture has fewer than two works")
	}
	svc := newService(t, bs)
	one := nqOf(t, svc, all[:1])
	everything := nqOf(t, svc, all)

	if len(one) == 0 {
		t.Fatal("single-work export is empty")
	}
	if !bytes.Contains(everything, one) {
		t.Fatalf("the work's lines changed when the other works joined the export:\n%s", one)
	}
}
