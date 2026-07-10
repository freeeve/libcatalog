package bibframe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/freeeve/libcat/storage"
	"github.com/freeeve/libcodex/rdf"
)

func TestGrainBlankPrefix(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"data/works/61/wl3gufu6q6bple.nq", "wl3gufu6q6bple_"},
		{"data/authorities/vocab/lcgft.nq", "lcgft_"},
		{"wone.nq", "wone_"},
		// Anything outside [A-Za-z0-9] folds to '_': a raw '.' or '-' in a label
		// position would produce a document no parser accepts.
		{"data/works/aa/odd.name-here.nq", "odd_name_here_"},
	} {
		if got := GrainBlankPrefix(tc.path); got != tc.want {
			t.Errorf("GrainBlankPrefix(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// A blank node can only appear where a term can. The scan has to know where terms
// are, because `_:c14n0` inside a literal or an IRI is text, not a node.
func TestRelabelGrainBlanksOnlyTouchesTerms(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{
			"subject and object",
			"_:c14n0 <p> _:c14n1 <g> .\n",
			"_:w_c14n0 <p> _:w_c14n1 <g> .\n",
		},
		{
			"graph term",
			"<s> <p> <o> _:c14n2 .\n",
			"<s> <p> <o> _:w_c14n2 .\n",
		},
		{
			"a literal that says _:c14n0 is text",
			`<s> <p> "see _:c14n0 for details" <g> .` + "\n",
			`<s> <p> "see _:c14n0 for details" <g> .` + "\n",
		},
		{
			"an IRI that contains _: is not a blank node",
			"<https://ex.org/_:c14n0> <p> <o> <g> .\n",
			"<https://ex.org/_:c14n0> <p> <o> <g> .\n",
		},
		{
			"an escaped quote does not end the literal early",
			`<s> <p> "a \" _:c14n0" <g> .` + "\n",
			`<s> <p> "a \" _:c14n0" <g> .` + "\n",
		},
		{
			"a typed literal keeps its datatype IRI",
			`_:c14n0 <p> "5"^^<http://www.w3.org/2001/XMLSchema#integer> <g> .` + "\n",
			`_:w_c14n0 <p> "5"^^<http://www.w3.org/2001/XMLSchema#integer> <g> .` + "\n",
		},
		{
			"a language-tagged literal keeps its tag",
			`_:c14n0 <p> "hola"@es <g> .` + "\n",
			`_:w_c14n0 <p> "hola"@es <g> .` + "\n",
		},
		{
			// Whitespace before the terminator is conventional, not required by
			// the grammar; a label may contain '.' but never end with one.
			"a dot terminator with no preceding space is not part of the label",
			"<s> <p> _:c14n0.\n",
			"<s> <p> _:w_c14n0.\n",
		},
		{
			"a dot inside a label is kept",
			"<s> <p> _:a.b <g> .\n",
			"<s> <p> _:w_a.b <g> .\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(RelabelGrainBlanks([]byte(tc.in), "w_")); got != tc.want {
				t.Errorf("\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// The relabeled document must still parse, and must carry the same statements.
func TestRelabelGrainBlanksStaysParseable(t *testing.T) {
	in := `_:c14n0 <http://ex.org/p> "quoted \" and _:c14n9 inside"@en <http://ex.org/g> .
<http://ex.org/s> <http://ex.org/p> _:c14n0 <http://ex.org/g> .
`
	out := RelabelGrainBlanks([]byte(in), "wone_")
	ds, err := rdf.ParseNQuads(out)
	if err != nil {
		t.Fatalf("relabeled document does not parse: %v\n%s", err, out)
	}
	if len(ds.Quads) != 2 {
		t.Fatalf("parsed %d quads, want 2", len(ds.Quads))
	}
	// One blank node, named once, referenced twice.
	if s := ds.Quads[0].S; !s.IsBlank() || s.Value != "wone_c14n0" {
		t.Errorf("subject = %+v, want blank wone_c14n0", s)
	}
	if o := ds.Quads[1].O; !o.IsBlank() || o.Value != "wone_c14n0" {
		t.Errorf("object = %+v, want blank wone_c14n0", o)
	}
	// The literal's text is untouched, including the `_:c14n9` inside it.
	if lit := ds.Quads[0].O; !strings.Contains(lit.Value, "_:c14n9") {
		t.Errorf("literal lost its text: %q", lit.Value)
	}
}

// writeGrain drops a raw .nq file into the tree SerializeGrains walks.
func writeGrain(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The property that kills the churn: a grain contributes the same bytes to the
// merge whether it is merged alone or beside sixty thousand others. Under the old
// scheme its labels were assigned by a running counter over the whole traversal,
// so adding an unrelated grain renamed this one's blank nodes (tasks/291).
func TestGrainLinesDoNotDependOnTheOtherGrains(t *testing.T) {
	const one = "_:c14n0 <http://ex.org/p> <http://ex.org/o> <http://ex.org/g> .\n"
	const two = "_:c14n0 <http://ex.org/q> <http://ex.org/o> <http://ex.org/g> .\n"

	alone := t.TempDir()
	writeGrain(t, alone, "data/works/aa/wtarget.nq", one)
	if _, err := SerializeGrains(alone, storage.Dir(alone)); err != nil {
		t.Fatal(err)
	}
	soloBytes, err := os.ReadFile(filepath.Join(alone, "catalog.nq"))
	if err != nil {
		t.Fatal(err)
	}

	// The same grain, now merged after another grain that also owns a _:c14n0.
	crowd := t.TempDir()
	writeGrain(t, crowd, "data/works/aa/wtarget.nq", one)
	writeGrain(t, crowd, "data/works/bb/wearlier.nq", two) // sorts before wtarget
	if _, err := SerializeGrains(crowd, storage.Dir(crowd)); err != nil {
		t.Fatal(err)
	}
	crowdBytes, err := os.ReadFile(filepath.Join(crowd, "catalog.nq"))
	if err != nil {
		t.Fatal(err)
	}

	solo := strings.TrimSpace(string(soloBytes))
	if !strings.Contains(string(crowdBytes), solo) {
		t.Fatalf("the grain's line changed when another grain joined the merge:\n alone: %s\n crowd: %s", solo, crowdBytes)
	}
	// And the two grains' identically-named blank nodes did not merge.
	if strings.Count(string(crowdBytes), "_:wtarget_c14n0") != 1 ||
		strings.Count(string(crowdBytes), "_:wearlier_c14n0") != 1 {
		t.Fatalf("blank nodes were not namespaced per grain:\n%s", crowdBytes)
	}
}

// rdf.Encoder opens a fresh blank-node scope per graph, so the old merge split a
// blank node a grain states in two graphs into two nodes. One node, by dataset
// semantics; one label, now.
func TestBlankNodeSharedAcrossGraphsKeepsOneLabel(t *testing.T) {
	dir := t.TempDir()
	writeGrain(t, dir, "data/works/aa/wshared.nq",
		"<http://ex.org/s> <http://ex.org/p> _:c14n0 <http://ex.org/g1> .\n"+
			"<http://ex.org/t> <http://ex.org/p> _:c14n0 <http://ex.org/g2> .\n")
	if _, err := SerializeGrains(dir, storage.Dir(dir)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "catalog.nq"))
	if err != nil {
		t.Fatal(err)
	}
	ds, err := rdf.ParseNQuads(got)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for _, q := range ds.Quads {
		if q.O.IsBlank() {
			labels[q.O.Value] = true
		}
	}
	if len(labels) != 1 {
		t.Fatalf("one grain node became %d document nodes: %v\n%s", len(labels), labels, got)
	}
}

// Labels are namespaced by grain id, so two grains sharing an id would merge their
// blank nodes into one -- a wrong graph, quietly. Fail loudly instead.
func TestSerializeGrainsRefusesDuplicateGrainIDs(t *testing.T) {
	dir := t.TempDir()
	writeGrain(t, dir, "data/works/aa/wdup.nq", "<http://ex.org/s> <http://ex.org/p> _:c14n0 <http://ex.org/g> .\n")
	writeGrain(t, dir, "data/works/bb/wdup.nq", "<http://ex.org/t> <http://ex.org/p> _:c14n0 <http://ex.org/g> .\n")

	_, err := SerializeGrains(dir, storage.Dir(dir))
	if err == nil {
		t.Fatal("two grains with the same id merged silently")
	}
	if !strings.Contains(err.Error(), "wdup") || !strings.Contains(err.Error(), "blank nodes would merge") {
		t.Errorf("error does not say what is wrong: %v", err)
	}
}
