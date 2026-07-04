package vocab

import (
	"errors"
	"os"
	"strings"
	"testing"
	"unicode"

	"github.com/freeeve/libcatalog/storage/blob"
)

func loadFixture(t *testing.T, schemes []string) *Index {
	t.Helper()
	data, err := os.ReadFile("testdata/authorities.nq")
	if err != nil {
		t.Fatal(err)
	}
	st := blob.NewMem()
	if _, err := st.Put(t.Context(), "data/authorities/ho/vocab.nq", data, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := Load(t.Context(), st, "data/authorities/", schemes)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestLoadAndLookup(t *testing.T) {
	ix := loadFixture(t, nil)
	if got := ix.Schemes(); len(got) != 2 || got[0] != "homosaurus" || got[1] != "lcsh" {
		t.Fatalf("schemes = %v", got)
	}
	term, ok := ix.Lookup("homosaurus", "https://homosaurus.org/v4/homoit0001235")
	if !ok {
		t.Fatal("transgender people not found")
	}
	if term.Labels["en"] != "Transgender people" || term.Labels["es"] != "Personas transgénero" {
		t.Fatalf("labels = %v", term.Labels)
	}
	if len(term.Broader) != 1 || term.Broader[0] != "https://homosaurus.org/v4/homoit0000508" {
		t.Fatalf("broader = %v", term.Broader)
	}
	if term.AltLabels["en"][0] != "Trans people" {
		t.Fatalf("alt = %v", term.AltLabels)
	}
	if !strings.HasPrefix(term.Definition["en"], "People whose gender identity") {
		t.Fatalf("definition = %v", term.Definition)
	}
	if term.Label("es") != "Personas transgénero" || term.Label("fr") != "Transgender people" {
		t.Fatalf("Label() fallbacks: es=%q fr=%q", term.Label("es"), term.Label("fr"))
	}
	parent, ok := ix.Lookup("homosaurus", "https://homosaurus.org/v4/homoit0000508")
	if !ok || len(parent.Narrower) != 1 {
		t.Fatalf("parent narrower = %+v", parent)
	}
	// rdfs:label is a fallback, prefLabel wins.
	lcsh, ok := ix.Lookup("lcsh", "http://id.loc.gov/authorities/subjects/sh85118553")
	if !ok || lcsh.Labels["en"] != "Science fiction" {
		t.Fatalf("lcsh = %+v", lcsh)
	}
	// Quads outside authority: graphs never load.
	if _, ok := ix.Lookup("homosaurus", "http://example.org/not-authority"); ok {
		t.Fatal("feed-graph noise indexed")
	}
	// Unknown scheme/id fail closed.
	if _, ok := ix.Lookup("fast", "anything"); ok {
		t.Fatal("unknown scheme resolved")
	}
	if _, ok := ix.Lookup("homosaurus", "https://homosaurus.org/v4/nope"); ok {
		t.Fatal("unknown term resolved")
	}
}

func TestSchemeFilter(t *testing.T) {
	ix := loadFixture(t, []string{"homosaurus"})
	if got := ix.Schemes(); len(got) != 1 || got[0] != "homosaurus" {
		t.Fatalf("schemes = %v", got)
	}
	if _, ok := ix.Lookup("lcsh", "http://id.loc.gov/authorities/subjects/sh85118553"); ok {
		t.Fatal("filtered scheme loaded")
	}
}

func TestSearch(t *testing.T) {
	ix := loadFixture(t, nil)
	// Prefix match on prefLabel, case-insensitive.
	hits := ix.Search("homosaurus", "trans", 10)
	if len(hits) != 1 || hits[0].ID != "https://homosaurus.org/v4/homoit0001235" {
		t.Fatalf("search trans = %v", hits)
	}
	// Alt labels searchable, result deduped with the pref hit.
	hits = ix.Search("homosaurus", "Trans people", 10)
	if len(hits) != 1 {
		t.Fatalf("alt search = %v", hits)
	}
	// Multilingual.
	hits = ix.Search("homosaurus", "personas", 10)
	if len(hits) != 1 || hits[0].Labels["es"] != "Personas transgénero" {
		t.Fatalf("es search = %v", hits)
	}
	// Limit respected.
	if hits := ix.Search("homosaurus", "", 10); hits != nil {
		t.Fatalf("empty query = %v", hits)
	}
	all := ix.Search("homosaurus", "q", 1)
	if len(all) != 1 {
		t.Fatalf("limit = %v", all)
	}
	if hits := ix.Search("lcsh", "science", 10); len(hits) != 1 {
		t.Fatalf("lcsh search = %v", hits)
	}
	if hits := ix.Search("nope", "x", 10); hits != nil {
		t.Fatalf("unknown scheme search = %v", hits)
	}
}

func TestMatchLabel(t *testing.T) {
	ix := loadFixture(t, nil)
	// Whole-heading pref match, case/whitespace-insensitive.
	hits := ix.MatchLabel("homosaurus", "  transgender   PEOPLE ")
	if len(hits) != 1 || hits[0].Term.ID != "https://homosaurus.org/v4/homoit0001235" || hits[0].Alt {
		t.Fatalf("pref match = %+v", hits)
	}
	// Alt-label match flagged as such.
	hits = ix.MatchLabel("homosaurus", "trans people")
	if len(hits) != 1 || !hits[0].Alt {
		t.Fatalf("alt match = %+v", hits)
	}
	// A prefix is not a whole heading.
	if hits := ix.MatchLabel("homosaurus", "trans"); hits != nil {
		t.Fatalf("prefix matched = %+v", hits)
	}
	if hits := ix.MatchLabel("homosaurus", ""); hits != nil {
		t.Fatalf("empty matched = %+v", hits)
	}
}

func TestReloadAndMergedTerms(t *testing.T) {
	data, err := os.ReadFile("testdata/authorities.nq")
	if err != nil {
		t.Fatal(err)
	}
	st := blob.NewMem()
	if _, err := st.Put(t.Context(), "data/authorities/ho/vocab.nq", data, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := Load(t.Context(), st, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Lookup("local", "https://example.org/local/a1"); ok {
		t.Fatal("local term present before write")
	}
	// A local grain lands (with an exactMatch and, later, a retirement);
	// the swapped snapshot serves it through the same *Index pointer.
	local := `<https://example.org/local/a1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Cozy fantasy"@en <authority:local> .
<https://example.org/local/a1> <http://www.w3.org/2004/02/skos/core#exactMatch> <http://id.loc.gov/authorities/subjects/sh1> <authority:local> .
`
	if _, err := st.Put(t.Context(), "data/authorities/aa/a1.nq", []byte(local), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Reload(t.Context(), st, "data/authorities/", nil); err != nil {
		t.Fatal(err)
	}
	term, ok := ix.Lookup("local", "https://example.org/local/a1")
	if !ok || len(term.ExactMatch) != 1 || term.ExactMatch[0] != "http://id.loc.gov/authorities/subjects/sh1" {
		t.Fatalf("local term after reload = %+v", term)
	}
	if hits := ix.Search("local", "cozy", 5); len(hits) != 1 {
		t.Fatalf("local search = %v", hits)
	}
	if all := ix.Terms("local"); len(all) != 1 || all[0].ID != "https://example.org/local/a1" {
		t.Fatalf("Terms = %+v", all)
	}
	// Retire the term: it leaves search but still resolves (old references
	// keep labeling), and Terms still lists it for the management screen.
	retired := local + `<https://example.org/local/a1> <https://github.com/freeeve/libcatalog/ns#mergedInto> <https://homosaurus.org/v4/homoit0001235> <authority:local> .
`
	if _, err := st.Put(t.Context(), "data/authorities/aa/a1.nq", []byte(retired), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := ix.Reload(t.Context(), st, "data/authorities/", nil); err != nil {
		t.Fatal(err)
	}
	term, ok = ix.Lookup("local", "https://example.org/local/a1")
	if !ok || term.MergedInto != "https://homosaurus.org/v4/homoit0001235" {
		t.Fatalf("retired lookup = %+v", term)
	}
	if hits := ix.Search("local", "cozy", 5); hits != nil {
		t.Fatalf("retired term still searchable = %v", hits)
	}
	if hits := ix.MatchLabel("local", "cozy fantasy"); hits != nil {
		t.Fatalf("retired term still matchable = %v", hits)
	}
	if all := ix.Terms("local"); len(all) != 1 {
		t.Fatalf("retired term missing from Terms = %+v", all)
	}
}

// TestPath drives the tasks/079 breadcrumb walk: shortest broader chain,
// root → parent order, polyhierarchy tie-breaks, and cycle/dangling safety.
func TestPath(t *testing.T) {
	nq := func(s, p, o string) string {
		if strings.HasPrefix(o, "http") {
			return "<" + s + "> <" + p + "> <" + o + "> <authority:t> .\n"
		}
		return "<" + s + "> <" + p + "> \"" + o + "\"@en <authority:t> .\n"
	}
	const pref = "http://www.w3.org/2004/02/skos/core#prefLabel"
	const broad = "http://www.w3.org/2004/02/skos/core#broader"
	// root ← mid ← leaf, plus leaf ← alt (alt is itself a root): the chain
	// through alt is shorter. deep chains only through mid.
	data := nq("http://t/root", pref, "Root") +
		nq("http://t/mid", pref, "Mid") + nq("http://t/mid", broad, "http://t/root") +
		nq("http://t/alt", pref, "Alt") +
		nq("http://t/leaf", pref, "Leaf") +
		nq("http://t/leaf", broad, "http://t/mid") +
		nq("http://t/leaf", broad, "http://t/alt") +
		// Two equal-length chains: parents sort pa < pb, pa must win.
		nq("http://t/pa", pref, "PA") + nq("http://t/pb", pref, "PB") +
		nq("http://t/tie", pref, "Tie") +
		nq("http://t/tie", broad, "http://t/pb") +
		nq("http://t/tie", broad, "http://t/pa") +
		// A two-node cycle with no root above it.
		nq("http://t/c1", pref, "C1") + nq("http://t/c1", broad, "http://t/c2") +
		nq("http://t/c2", pref, "C2") + nq("http://t/c2", broad, "http://t/c1") +
		// A term whose only parent is not in the vocabulary.
		nq("http://t/dangling", pref, "Dangling") +
		nq("http://t/dangling", broad, "http://elsewhere/gone")
	st := blob.NewMem()
	if _, err := st.Put(t.Context(), "a/t.nq", []byte(data), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := Load(t.Context(), st, "a/", nil)
	if err != nil {
		t.Fatal(err)
	}
	labels := func(path []TermRef) string {
		parts := make([]string, len(path))
		for i, p := range path {
			parts[i] = p.Label
		}
		return strings.Join(parts, " > ")
	}
	// Shortest chain wins polyhierarchy: leaf goes through alt, not root/mid.
	if got := labels(ix.Path("t", "http://t/leaf")); got != "Alt" {
		t.Fatalf("leaf path = %q", got)
	}
	if got := labels(ix.Path("t", "http://t/mid")); got != "Root" {
		t.Fatalf("mid path = %q", got)
	}
	// Equal-length chains break ties by URI order.
	if got := labels(ix.Path("t", "http://t/tie")); got != "PA" {
		t.Fatalf("tie path = %q", got)
	}
	// Roots, cycles, dangling parents, and unknown terms all yield nil.
	for _, id := range []string{"http://t/root", "http://t/c1", "http://t/dangling", "http://t/nope"} {
		if got := ix.Path("t", id); got != nil {
			t.Fatalf("Path(%s) = %v, want nil", id, got)
		}
	}
	if got := ix.Path("nope", "http://t/leaf"); got != nil {
		t.Fatalf("unknown scheme path = %v", got)
	}
	// TermRefs carry scheme and URI for the UI to link through.
	path := ix.Path("t", "http://t/mid")
	if len(path) != 1 || path[0].Scheme != "t" || path[0].ID != "http://t/root" {
		t.Fatalf("path refs = %+v", path)
	}
}

func TestNormalizeFolk(t *testing.T) {
	good := map[string]string{
		"Cozy Fantasy":      "cozy fantasy",
		"  found\tfamily  ": "found family",
		"SAPPHIC":           "sapphic",
		"enemies to lovers": "enemies to lovers",
		"ＦＵＬＬＷＩＤＴＨ":         "fullwidth", // NFKC folds fullwidth forms
		"ace rep é":         "ace rep é",
	}
	for raw, want := range good {
		got, err := NormalizeFolk(raw)
		if err != nil || got != want {
			t.Errorf("NormalizeFolk(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	bad := []string{
		"", "a", strings.Repeat("x", 61),
		"see http://spam.example/x", "www.spam.example",
		"<script>alert(1)</script>", "tag\x00null", "tab\ttag\ncontrol\x1b",
		"{template}",
	}
	for _, raw := range bad {
		if got, err := NormalizeFolk(raw); !errors.Is(err, ErrBadFolkTerm) {
			t.Errorf("NormalizeFolk(%q) = %q, %v; want ErrBadFolkTerm", raw, got, err)
		}
	}
}

func FuzzNormalizeFolk(f *testing.F) {
	for _, seed := range []string{"cozy fantasy", "É", "a\x00b", "http://x", strings.Repeat("y", 80)} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		out, err := NormalizeFolk(raw)
		if err != nil {
			return
		}
		// Invariants of any accepted term.
		if out != strings.ToLower(out) {
			t.Fatalf("not lowercase: %q", out)
		}
		if strings.Contains(out, "  ") || out != strings.TrimSpace(out) {
			t.Fatalf("whitespace not collapsed: %q", out)
		}
		for _, r := range out {
			if unicode.IsControl(r) {
				t.Fatalf("control char survived: %q", out)
			}
		}
		if n := len([]rune(out)); n < folkMinLen || n > folkMaxLen {
			t.Fatalf("length out of bounds: %q", out)
		}
		// Idempotent.
		again, err := NormalizeFolk(out)
		if err != nil || again != out {
			t.Fatalf("not idempotent: %q -> %q (%v)", out, again, err)
		}
	})
}
