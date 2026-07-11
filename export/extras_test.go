package export

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"
)

// plantExtra appends one lcat:extra/<key> quad to the first Work's grain, the
// way an ingest mapping or an editorial pass leaves one, and returns that Work's
// id. It goes in a grain, not in catalog.nq: the exporter derives the nq download
// from the grains, so a quad that exists only in catalog.nq exists
// nowhere the exporter looks.
func plantExtra(t *testing.T, root, key, value string) string {
	t.Helper()
	paths, err := grainPaths(root)
	if err != nil || len(paths) == 0 {
		t.Fatalf("no grains under %s: %v", root, err)
	}
	id := strings.TrimSuffix(filepath.Base(paths[0]), ".nq")
	quad := "<" + bibframe.WorkIRI(id) + "> <" + bibframe.ExtraPred + key + "> \"" + value + "\" <editorial:> .\n"
	f, err := os.OpenFile(paths[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(quad); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return id
}

// holdingsCorpus is one Work carrying the three shapes of extra that matter:
// a private holdings flag, a public one, and a provenance list with its own
// allowlist.
func holdingsCorpus(t *testing.T) string {
	t.Helper()
	in := corpus(t, bookRecord("c1", "9780000000011", "Author, A.", "First Book"))
	plantExtra(t, in, "inQll", "true")
	plantExtra(t, in, "rating", "4.2")
	plantSourcesQuad(t, in, "loc, mombian")
	return in
}

// nqOf runs an export and returns the nq download's text.
func nqOf(t *testing.T, in string, opts Options) string {
	t.Helper()
	opts.In, opts.Out = in, t.TempDir()
	if opts.Log == nil {
		opts.Log = io.Discard
	}
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	return gunzip(t, filepath.Join(opts.Out, "catalog.nq.gz"))
}

// the nq download is a public surface, so the extra-key allowlist has
// to govern it the way it governs catalog.json. A private holdings flag ("this
// library already has it") must not ride the RDF out.
//
// The `rating` assertion is the control: an absence is not evidence of a filter,
// and a writeNQ that emitted no extras at all would satisfy the inQll check alone.
func TestNQDownloadDropsExtrasOffTheAllowlist(t *testing.T) {
	nq := nqOf(t, holdingsCorpus(t), Options{PublicExtras: map[string]bool{"rating": true}})
	if strings.Contains(nq, "inQll") {
		t.Errorf("the private extra leaked into the nq download:\n%s", nq)
	}
	if !strings.Contains(nq, "rating") {
		t.Errorf("the allowlisted extra was dropped too; the filter drops everything:\n%s", nq)
	}
}

// Absent allowlist keeps everything -- today's behavior, and no silent break for
// a deployment that never configures one.
func TestNQDownloadKeepsEveryExtraWithoutAnAllowlist(t *testing.T) {
	nq := nqOf(t, holdingsCorpus(t), Options{})
	for _, key := range []string{"inQll", "rating", "sources"} {
		if !strings.Contains(nq, key) {
			t.Errorf("extra %q dropped without an allowlist:\n%s", key, nq)
		}
	}
}

// `sources` is governed by public-sources, filtered by value. A public-extras list
// that does not name it must not drop the key -- that would let one allowlist
// silently undo the other, and the download would disclose *less* provenance than
// the operator configured.
func TestPublicExtrasNeverDropsSources(t *testing.T) {
	nq := nqOf(t, holdingsCorpus(t), Options{PublicExtras: map[string]bool{"rating": true}})
	if !strings.Contains(nq, "loc, mombian") {
		t.Errorf("public-extras dropped the sources quad it does not govern:\n%s", nq)
	}
}

// Both allowlists at once: sources narrows within its literal, the private key
// goes, the public key stays. This is the configuration a real deployment runs.
func TestBothAllowlistsCompose(t *testing.T) {
	nq := nqOf(t, holdingsCorpus(t), Options{
		PublicSources: map[string]bool{"loc": true},
		PublicExtras:  map[string]bool{"rating": true},
	})
	if strings.Contains(nq, "mombian") {
		t.Errorf("private source leaked:\n%s", nq)
	}
	if !strings.Contains(nq, `"loc"`) {
		t.Errorf("public source stripped:\n%s", nq)
	}
	if strings.Contains(nq, "inQll") {
		t.Errorf("private extra leaked:\n%s", nq)
	}
	if !strings.Contains(nq, "rating") {
		t.Errorf("public extra stripped:\n%s", nq)
	}
}

// A cover the public catalog cannot name is a cover no public page renders, so
// publishing the blob would disclose exactly what the allowlist withheld -- the
// class of leak. The covers directory is not written at all, and the
// build says why rather than leaving an operator to wonder.
func TestCoversAreWithheldWhenCoverIsNotAllowlisted(t *testing.T) {
	in := corpus(t, bookRecord("c1", "9780000000011", "Author, A.", "First Book"))
	id := workIDs(t, in)[0]
	plantCover(t, in, id)

	coversOut := filepath.Join(t.TempDir(), "covers")
	var log bytes.Buffer
	if _, err := Run(Options{
		In: in, Out: t.TempDir(), CoversOut: coversOut, Log: &log,
		PublicExtras: map[string]bool{"rating": true},
	}); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(coversOut); err == nil && len(entries) > 0 {
		t.Errorf("published %d covers the public catalog cannot name: %v", len(entries), entries)
	}
	if !strings.Contains(log.String(), "not published") {
		t.Errorf("withholding the covers was silent: %q", log.String())
	}
}

// The control for the test above: with `cover` allowlisted, the same corpus
// publishes the same blob. Without this, a copyCovers that never ran would pass.
func TestCoversArePublishedWhenCoverIsAllowlisted(t *testing.T) {
	in := corpus(t, bookRecord("c1", "9780000000011", "Author, A.", "First Book"))
	id := workIDs(t, in)[0]
	plantCover(t, in, id)

	coversOut := filepath.Join(t.TempDir(), "covers")
	if _, err := Run(Options{
		In: in, Out: t.TempDir(), CoversOut: coversOut, Log: io.Discard,
		PublicExtras: map[string]bool{bibframe.CoverExtraKey: true},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(coversOut, id+".png")); err != nil {
		t.Errorf("the allowlisted cover was not published: %v", err)
	}
}

// extraKey reads N-Quads field 2. A literal that quotes an extra IRI is an
// object, not a predicate, and filtering on a substring search would drop the
// quad -- silently deleting real data that merely mentions the namespace.
func TestExtraKeyReadsThePredicateNotTheObject(t *testing.T) {
	pred := "<" + bibframe.ExtraPred + "inQll>"
	// The IRI is followed by a space *inside* the literal. A substring search
	// finds it, cuts at that space, and reads a predicate that is really an
	// object -- silently deleting a note whose only crime is quoting the
	// namespace. An IRI at the end of a literal does not catch this: the closing
	// quote makes the substring parse fail by luck, and the test passes for the
	// wrong reason.
	quoting := `<#wxWork> <http://ex.org/note> "see ` + pred + ` for details" <editorial:> .`
	cases := []struct {
		line, want string
	}{
		{`<#wxWork> ` + pred + ` "true" <editorial:> .`, "inQll"},
		{quoting, ""},
		{`<#wxWork> <http://ex.org/note> "see ` + pred + `" <editorial:> .`, ""},
		{`<#wxWork> <http://ex.org/note> "plain" <editorial:> .`, ""},
		{`<#wxWork>`, ""},
		{``, ""},
	}
	for _, tc := range cases {
		if got := extraKey(tc.line); got != tc.want {
			t.Errorf("extraKey(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
	// And the filter agrees: the note survives an allowlist that excludes inQll.
	f := nqFilter{extras: map[string]bool{"rating": true}}
	line, keep := f.apply(quoting)
	if !keep || line != quoting {
		t.Errorf("a note quoting the extra namespace was filtered as an extra")
	}
}

// FuzzExtraKey checks the predicate reader never panics and never claims a key
// from a line whose second field is not an lcat:extra/ IRI.
func FuzzExtraKey(f *testing.F) {
	f.Add(`<#wxWork> <` + bibframe.ExtraPred + `inQll> "true" <editorial:> .`)
	f.Add(`<#wxWork> <http://ex.org/p> "v" <g> .`)
	f.Add(`<` + bibframe.ExtraPred + `x> <p> "v" <g> .`)
	f.Add(` `)
	f.Add(``)
	f.Fuzz(func(t *testing.T, line string) {
		key := extraKey(line)
		if key == "" {
			return
		}
		if strings.ContainsAny(key, " <>") {
			t.Fatalf("extraKey(%q) = %q, which is not an IRI path segment", line, key)
		}
		if !strings.Contains(line, bibframe.ExtraPred+key+">") {
			t.Fatalf("extraKey(%q) = %q, which the line does not contain as a predicate", line, key)
		}
	})
}
