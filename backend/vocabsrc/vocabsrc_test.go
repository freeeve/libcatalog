package vocabsrc

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

const zinesNT = `<http://id.loc.gov/authorities/genreForms/gf2014026266> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://www.w3.org/2004/02/skos/core#Concept> .
<http://id.loc.gov/authorities/genreForms/gf2014026266> <http://www.w3.org/2004/02/skos/core#prefLabel> "Zines"@en .
<http://id.loc.gov/authorities/genreForms/gf2014026266> <http://www.w3.org/2004/02/skos/core#altLabel> "Fanzines"@en .
<http://id.loc.gov/authorities/genreForms/gf2014026266> <http://www.w3.org/2004/02/skos/core#broader> <http://id.loc.gov/authorities/genreForms/gf2014026135> .
<http://id.loc.gov/authorities/genreForms/gf2014026135> <http://www.w3.org/2004/02/skos/core#prefLabel> "Periodicals"@en .
`

func newService(t *testing.T) *Service {
	t.Helper()
	return &Service{DB: store.NewMem(), Blob: blob.NewMem(), AuthoritiesPrefix: "data/authorities/"}
}

func TestBuiltinsListed(t *testing.T) {
	s := newService(t)
	sources, err := s.Sources(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Source{}
	for _, src := range sources {
		byName[src.Name] = src
	}
	for _, name := range []string{"lcsh", "lcgft", "lcshac", "lcnaf", "fast", "wikidata", "viaf"} {
		src, ok := byName[name]
		if !ok || !src.Builtin {
			t.Fatalf("builtin %s missing or unmarked: %+v", name, src)
		}
	}
	if byName["lcnaf"].CanSnapshot() {
		t.Error("lcnaf must be live-only (11M concepts)")
	}
	if !byName["lcgft"].CanSnapshot() || !byName["lcgft"].CanSuggest() {
		t.Error("lcgft should suggest and snapshot")
	}
	// fast is live-only like lcnaf (tasks/132: the full dump is ~2M concepts; a
	// corpus subset supplies display labels). Being suggest-capable it also
	// registers as a moderated enrichment target at boot (appdeps iterates
	// CanSuggest sources) -- no fast-specific wiring exists to test.
	if !byName["fast"].CanSuggest() || byName["fast"].CanSnapshot() {
		t.Errorf("fast should be suggest-only: %+v", byName["fast"])
	}
}

// TestViewsListsOrphanInstalls: a snapshot installed without a registered
// source (an offline vocab-install, tasks/163, or a mem registry that reset)
// still gets a Vocabularies-screen row, synthesized from its sidecar.
func TestViewsListsOrphanInstalls(t *testing.T) {
	s := newService(t)
	ctx := t.Context()
	if err := s.PutSource(ctx, Source{Name: "homosaurus", Scheme: "homosaurus"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallUpload(ctx, "homosaurus", strings.NewReader(zinesNT)); err != nil {
		t.Fatal(err)
	}
	// The registry forgets the source; the blob-side install remains. Straight to
	// the store, because DeleteSource now refuses to produce this state (tasks/255)
	// -- the routes that still reach it are the two this test's doc comment names.
	if err := s.DB.Delete(ctx, store.Record{Key: sourceKey("homosaurus")}, store.CondNone); err != nil {
		t.Fatal(err)
	}
	views, err := s.Views(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var orphan *SourceView
	for i := range views {
		if views[i].Name == "homosaurus" {
			orphan = &views[i]
		}
	}
	if orphan == nil {
		t.Fatal("orphan install missing from views")
	}
	if orphan.Scheme != "homosaurus" || orphan.Installed == nil || orphan.Installed.Terms == 0 {
		t.Fatalf("orphan view = %+v", orphan)
	}
	// Still removable through the normal path.
	if err := s.RemoveSnapshot(ctx, "homosaurus"); err != nil {
		t.Fatal(err)
	}
}

func TestPutSourceOverridesAndValidates(t *testing.T) {
	s := newService(t)
	ctx := t.Context()
	// Drop-in custom source.
	custom := Source{
		Name: "homosaurus", Scheme: "homosaurus", License: "CC-BY-NC-ND",
		SnapshotURL: "https://homosaurus.org/v4.nt",
	}
	if err := s.PutSource(ctx, custom); err != nil {
		t.Fatal(err)
	}
	// Override a builtin's snapshot URL; the merged view keeps Builtin.
	lcgft := Source{Name: "lcgft", Scheme: "lcgft", SnapshotURL: "https://mirror.example/lcgft.nt.gz"}
	if err := s.PutSource(ctx, lcgft); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSource(ctx, "lcgft")
	if err != nil {
		t.Fatal(err)
	}
	if got.SnapshotURL != lcgft.SnapshotURL || !got.Builtin {
		t.Fatalf("override not applied or builtin flag lost: %+v", got)
	}
	// Deleting the override restores the shipped definition.
	if err := s.DeleteSource(ctx, "lcgft"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSource(ctx, "lcgft")
	if !strings.Contains(got.SnapshotURL, "id.loc.gov") {
		t.Fatalf("builtin not restored: %+v", got)
	}
	// A builtin without an override cannot be deleted.
	if err := s.DeleteSource(ctx, "viaf"); !errors.Is(err, ErrValidation) {
		t.Fatalf("want ErrValidation deleting builtin, got %v", err)
	}
	for _, bad := range []Source{
		{Name: "", Scheme: "x", SnapshotURL: "https://x"},
		{Name: "a/b", Scheme: "x", SnapshotURL: "https://x"},
		{Name: "x", Scheme: "", SnapshotURL: "https://x"},
		{Name: "x", Scheme: "x", SuggestURL: "https://x", SuggestFlavor: "nope"},
		{Name: "x", Scheme: "x", SnapshotURL: "ftp://x"},
	} {
		if err := s.PutSource(ctx, bad); !errors.Is(err, ErrValidation) {
			t.Errorf("want ErrValidation for %+v, got %v", bad, err)
		}
	}
	// Neither suggest nor snapshot is fine: such a source installs by
	// hand-uploaded dump.
	if err := s.PutSource(ctx, Source{Name: "uploadonly", Scheme: "x"}); err != nil {
		t.Fatalf("upload-only source refused: %v", err)
	}
}

func TestSuggestFlavors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/authorities/subjects/suggest2"):
			_, _ = w.Write([]byte(`{"hits":[{"uri":"http://id.loc.gov/authorities/subjects/sh85118553","aLabel":"Science fiction","more":{"variantLabels":["Sci-fi","Science fiction (Literary genre)"]}}]}`))
		case r.URL.Path == "/w/api.php":
			_, _ = w.Write([]byte(`{"search":[{"id":"Q24925","label":"science fiction","description":"genre of speculative fiction","concepturi":"http://www.wikidata.org/entity/Q24925"}]}`))
		case r.URL.Path == "/viaf/AutoSuggest":
			_, _ = w.Write([]byte(`{"result":[{"term":"Le Guin, Ursula K.","displayForm":"Le Guin, Ursula K., 1929-2018","nametype":"personal","viafid":"66475792","lc":"n  79021164","dnb":"118570803","wkp":"Q181659"}]}`))
		case r.URL.Path == "/searchfast/fastsuggest":
			if r.URL.Query().Get("queryIndex") != "suggestall" || r.URL.Query().Get("wt") != "json" {
				http.Error(w, "bad request", http.StatusBadRequest) // the bare form 400s on the real service
				return
			}
			_, _ = w.Write([]byte(`{"response":{"numFound":2,"docs":[{"idroot":["fst01108566"],"tag":150,"suggestall":["Sci-fi"],"auth":"Science fiction"},{"idroot":["fst01726489"],"tag":155,"suggestall":["Science fiction"],"auth":"Science fiction"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := &SuggestClient{Client: srv.Client()}
	ctx := t.Context()

	s2, err := c.Suggest(ctx, Source{
		Name: "lcsh", Scheme: "lcsh",
		SuggestFlavor: FlavorSuggest2, SuggestURL: srv.URL, SuggestDataset: "authorities/subjects",
	}, "science fiction", 5)
	if err != nil || len(s2) != 1 || s2[0].Label != "Science fiction" || s2[0].Source != "lcsh" {
		t.Fatalf("suggest2: %v %+v", err, s2)
	}
	if len(s2[0].Variants) != 2 || s2[0].Variants[0] != "Sci-fi" {
		t.Fatalf("suggest2 variants: %+v", s2[0].Variants)
	}

	wd, err := c.Suggest(ctx, Source{
		Name: "wikidata", Scheme: "wikidata", SuggestFlavor: FlavorWikidata, SuggestURL: srv.URL,
	}, "science fiction", 5)
	if err != nil || len(wd) != 1 || wd[0].ID != "http://www.wikidata.org/entity/Q24925" || wd[0].Description == "" {
		t.Fatalf("wikidata: %v %+v", err, wd)
	}

	// searchfast (tasks/132): fst-prefixed, zero-padded idroot maps to the
	// canonical id.worldcat.org URI; auth is the label with a matched variant
	// form kept; the MARC tag becomes the facet description (150 topical, 155
	// form/genre).
	ff, err := c.Suggest(ctx, Source{
		Name: "fast", Scheme: "fast", SuggestFlavor: FlavorSearchFAST, SuggestURL: srv.URL,
	}, "science fiction", 5)
	if err != nil || len(ff) != 2 {
		t.Fatalf("searchfast: %v %+v", err, ff)
	}
	if ff[0].ID != "http://id.worldcat.org/fast/1108566" || ff[0].Label != "Science fiction" || ff[0].Description != "topical" {
		t.Fatalf("searchfast hit 0: %+v", ff[0])
	}
	if len(ff[0].Variants) != 1 || ff[0].Variants[0] != "Sci-fi" {
		t.Fatalf("searchfast variants: %+v", ff[0].Variants)
	}
	if ff[1].ID != "http://id.worldcat.org/fast/1726489" || ff[1].Description != "form/genre" || len(ff[1].Variants) != 0 {
		t.Fatalf("searchfast hit 1: %+v", ff[1])
	}

	vf, err := c.Suggest(ctx, Source{
		Name: "viaf", Scheme: "viaf", SuggestFlavor: FlavorVIAF, SuggestURL: srv.URL,
	}, "le guin", 5)
	if err != nil || len(vf) != 1 || vf[0].ID != "http://viaf.org/viaf/66475792" {
		t.Fatalf("viaf: %v %+v", err, vf)
	}
	wantXM := []string{
		"http://id.loc.gov/authorities/names/n79021164",
		"https://d-nb.info/gnd/118570803",
		"http://www.wikidata.org/entity/Q181659",
	}
	if len(vf[0].ExactMatch) != 3 {
		t.Fatalf("viaf exactMatch: %+v", vf[0].ExactMatch)
	}
	for i, want := range wantXM {
		if vf[0].ExactMatch[i] != want {
			t.Errorf("exactMatch[%d] = %s, want %s", i, vf[0].ExactMatch[i], want)
		}
	}
}

func TestConvertFiltersAndTags(t *testing.T) {
	out, terms, err := Convert(strings.NewReader(zinesNT), "lcgft")
	if err != nil {
		t.Fatal(err)
	}
	if terms != 2 {
		t.Fatalf("terms = %d, want 2", terms)
	}
	text := string(out)
	if strings.Contains(text, "rdf-syntax-ns#type") {
		t.Error("rdf:type should be filtered")
	}
	if !strings.Contains(text, "<authority:lcgft>") {
		t.Errorf("quads not tagged with authority graph:\n%s", text)
	}
	if !strings.Contains(text, `"Zines"@en`) || !strings.Contains(text, `"Fanzines"@en`) {
		t.Errorf("labels missing:\n%s", text)
	}
	// Gzipped input converts identically.
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(zinesNT))
	_ = zw.Close()
	gzOut, gzTerms, err := Convert(&gz, "lcgft")
	if err != nil || gzTerms != terms || !bytes.Equal(gzOut, out) {
		t.Fatalf("gzip convert differs: err=%v terms=%d", err, gzTerms)
	}
	// A malformed line refuses the dump (tasks/317). It used to be skipped, which is
	// how a truncated download installed as a smaller vocabulary.
	if _, _, err := Convert(strings.NewReader("not rdf at all\n"+zinesNT), "lcgft"); err == nil {
		t.Fatal("a malformed line converted cleanly; a truncated dump would install silently")
	}
}

// The operator has to be able to find the bad line. The bad line sits past the first
// megabyte on purpose: that is where the old chunked bulk parser restarted its line
// numbering, and a running base had to correct it (tasks/317). The streaming decoder
// numbers from the start of the dump instead (tasks/320); this test is what says so
// from outside, and it fails the same way under either mistake.
func TestAMalformedLineIsReportedAtItsLineInTheWholeDump(t *testing.T) {
	var b strings.Builder
	// Enough well-formed statements to push the bad line past a bulk parser's chunk.
	const stmt = "<https://homosaurus.org/v3/homoit0000001> <http://www.w3.org/2004/02/skos/core#prefLabel> \"Filler\"@en .\n"
	for b.Len() < 3<<20 {
		b.WriteString(stmt)
	}
	good := strings.Count(b.String(), "\n")
	b.WriteString("<https://homosaurus.org/v3/homoit000\n") // cut mid-IRI, as a partial download is
	want := good + 1

	_, _, err := Convert(strings.NewReader(b.String()), "lcgft")
	if err == nil {
		t.Fatal("the truncated dump converted cleanly")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("line %d", want)) {
		t.Errorf("error names the wrong line (want %d): %v", want, err)
	}
	if !strings.Contains(err.Error(), "truncated or corrupt") {
		t.Errorf("the error does not tell the operator what is wrong: %v", err)
	}
}

func TestDownloadInstallRemoveLifecycle(t *testing.T) {
	dump := zinesNT
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/lcgft.nt.gz" {
			http.NotFound(w, r)
			return
		}
		zw := gzip.NewWriter(w)
		_, _ = zw.Write([]byte(dump))
		_ = zw.Close()
	}))
	defer srv.Close()

	s := newService(t)
	s.HTTPClient = srv.Client()
	ctx := t.Context()
	ix, err := vocab.Load(ctx, s.Blob, s.AuthoritiesPrefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Index = ix
	if err := s.PutSource(ctx, Source{
		Name: "lcgft", Scheme: "lcgft", SnapshotURL: srv.URL + "/lcgft.nt.gz",
	}); err != nil {
		t.Fatal(err)
	}

	job, err := s.CreateDownload(ctx, "eve@example.com", "lcgft")
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != StatusQueued {
		t.Fatalf("status = %s", job.Status)
	}
	if ran, err := s.RunQueued(ctx); err != nil || ran != 1 {
		t.Fatalf("RunQueued: ran=%d err=%v", ran, err)
	}
	job, err = s.GetJob(ctx, job.ID)
	if err != nil || job.Status != StatusDone || job.Terms != 2 {
		t.Fatalf("job after run: %+v err=%v", job, err)
	}
	// The index swapped in the terms: offline typeahead works.
	hits := ix.Search("lcgft", "zin", 5)
	if len(hits) != 1 || hits[0].Label("en") != "Zines" {
		t.Fatalf("search after install: %+v", hits)
	}
	if got := ix.Search("lcgft", "fanz", 5); len(got) != 1 {
		t.Fatalf("alt-label search: %+v", got)
	}
	// Views carry install state.
	views, err := s.Views(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var v *SourceView
	for i := range views {
		if views[i].Name == "lcgft" {
			v = &views[i]
		}
	}
	if v == nil || v.Installed == nil || v.Installed.Terms != 2 || v.Job == nil || v.Job.Status != StatusDone {
		t.Fatalf("view: %+v", v)
	}
	// Refresh: a changed upstream dump overwrites in place.
	dump = zinesNT + `<http://id.loc.gov/authorities/genreForms/gf9> <http://www.w3.org/2004/02/skos/core#prefLabel> "Chapbooks"@en .` + "\n"
	job2, err := s.CreateDownload(ctx, "eve@example.com", "lcgft")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunDownload(ctx, job2.ID); err != nil {
		t.Fatal(err)
	}
	if job2, _ = s.GetJob(ctx, job2.ID); job2.Terms != 3 {
		t.Fatalf("refresh terms = %d, want 3", job2.Terms)
	}
	if got := ix.Search("lcgft", "chapbook", 5); len(got) != 1 {
		t.Fatalf("refreshed term missing: %+v", got)
	}
	// Control: the install really did build sidecar artifacts, so their absence
	// after the removal below means the removal took them (tasks/252).
	if before := blobPaths(t, s.Blob, s.prefix()+"sidecar/"); len(before) == 0 {
		t.Fatal("no sidecar artifacts to remove -- the removal check below would pass vacuously")
	}
	// Remove: snapshot, meta and sidecar go, terms drop out of the index.
	if err := s.RemoveSnapshot(ctx, "lcgft"); err != nil {
		t.Fatal(err)
	}
	if got := ix.Search("lcgft", "zin", 5); len(got) != 0 {
		t.Fatalf("terms survive removal: %+v", got)
	}
	// This comment used to stand in for the assertion, and the artifacts stayed on
	// disk for as long as it did. 169MB of them, for a removed lcsh.
	if after := blobPaths(t, s.Blob, s.prefix()+"sidecar/"); len(after) != 0 {
		t.Errorf("RemoveSnapshot left the sidecar behind: %v", after)
	}
	if _, _, err := s.Blob.Get(ctx, s.snapshotPath("lcgft")); !errors.Is(err, blob.ErrNotFound) {
		t.Errorf("snapshot survives removal: %v", err)
	}
	if err := s.RemoveSnapshot(ctx, "lcgft"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double remove: %v", err)
	}
}

// blobPaths lists the object paths under prefix.
func blobPaths(t *testing.T, st blob.Store, prefix string) []string {
	t.Helper()
	var out []string
	for e, err := range st.List(t.Context(), prefix) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e.Path)
	}
	return out
}

// TestRemoveSnapshotCleansUpAfterAnOrphanedInstall is the leak the harness actually
// found: the source row is gone, so RemoveSnapshot cannot ask the registry what
// scheme to clean up and has to read the install meta instead. Views synthesizes
// this orphan install precisely so it stays removable (tasks/252).
func TestRemoveSnapshotCleansUpAfterAnOrphanedInstall(t *testing.T) {
	s := newService(t)
	ctx := t.Context()
	ix, err := vocab.Load(ctx, s.Blob, s.AuthoritiesPrefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Index = ix
	if err := s.PutSource(ctx, Source{Name: "zzleak", Scheme: "zzleak"}); err != nil {
		t.Fatal(err)
	}
	terms, err := s.InstallUpload(ctx, "zzleak", strings.NewReader(zzleakNT))
	if err != nil || terms != 1 {
		t.Fatalf("install: terms=%d err=%v", terms, err)
	}
	if before := blobPaths(t, s.Blob, s.prefix()+"sidecar/"); len(before) == 0 {
		t.Fatal("install built no sidecar artifacts")
	}

	// The registry loses the record without the blob store hearing about it: an
	// offline vocab-install, or a deployment whose registry reset. DeleteSource can
	// no longer produce this state (tasks/255), but these routes still do.
	if err := s.DB.Delete(ctx, store.Record{Key: sourceKey("zzleak")}, store.CondNone); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSource(ctx, "zzleak"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("source still registered: %v", err)
	}
	// Control: the snapshot outlived its source row, which is what makes this an
	// orphan rather than an ordinary removal.
	if _, _, err := s.Blob.Get(ctx, s.snapshotPath("zzleak")); err != nil {
		t.Fatalf("the snapshot went with the registry record: %v", err)
	}

	if err := s.RemoveSnapshot(ctx, "zzleak"); err != nil {
		t.Fatal(err)
	}
	if after := blobPaths(t, s.Blob, s.prefix()+"sidecar/"); len(after) != 0 {
		t.Errorf("orphan install left %d sidecar artifacts: %v", len(after), after)
	}
}

const zzleakNT = `<http://example.org/z/1> <http://www.w3.org/2004/02/skos/core#prefLabel> "Z"@en .
`

// TestDeleteSourceRefusesWhileASnapshotIsInstalled makes the screen's tooltip true:
// "an installed snapshot must be removed first". Nothing enforced it, so one click
// silently produced an orphan row offering two buttons that could only 404
// (tasks/255).
func TestDeleteSourceRefusesWhileASnapshotIsInstalled(t *testing.T) {
	s := newService(t)
	ctx := t.Context()
	ix, err := vocab.Load(ctx, s.Blob, s.AuthoritiesPrefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Index = ix
	if err := s.PutSource(ctx, Source{Name: "zzorph", Scheme: "zzorph"}); err != nil {
		t.Fatal(err)
	}
	// Control: with no snapshot installed, deleting is the ordinary path.
	if err := s.DeleteSource(ctx, "zzorph"); err != nil {
		t.Fatalf("delete of an uninstalled source: %v", err)
	}

	if err := s.PutSource(ctx, Source{Name: "zzorph", Scheme: "zzorph"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallUpload(ctx, "zzorph", strings.NewReader(zzleakNT)); err != nil {
		t.Fatal(err)
	}
	err = s.DeleteSource(ctx, "zzorph")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("delete with a snapshot installed: err = %v, want ErrConflict", err)
	}
	// The refusal has to say what to do first, or it is just a wall.
	if !strings.Contains(err.Error(), "remove it before deleting") {
		t.Errorf("refusal does not name the remedy: %v", err)
	}
	// It refused, so the source is still there and still usable.
	if _, err := s.GetSource(ctx, "zzorph"); err != nil {
		t.Fatalf("refused delete removed the source anyway: %v", err)
	}

	// The documented order works.
	if err := s.RemoveSnapshot(ctx, "zzorph"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSource(ctx, "zzorph"); err != nil {
		t.Fatalf("delete after remove: %v", err)
	}
}

// TestDeleteSourceStillDropsABuiltinOverrideWithASnapshotInstalled is the exemption.
// Deleting a stored override of a built-in restores the shipped definition rather
// than removing the row, so the install keeps a source and is never orphaned --
// refusing there would strand an admin who overrode lcsh and wants the default back.
func TestDeleteSourceStillDropsABuiltinOverrideWithASnapshotInstalled(t *testing.T) {
	s := newService(t)
	ctx := t.Context()
	ix, err := vocab.Load(ctx, s.Blob, s.AuthoritiesPrefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Index = ix
	if err := s.PutSource(ctx, Source{Name: "lcgft", Scheme: "lcgft", SnapshotURL: "http://example.invalid/x.nt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallUpload(ctx, "lcgft", strings.NewReader(zinesNT)); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSource(ctx, "lcgft"); err != nil {
		t.Fatalf("dropping a builtin override with a snapshot installed: %v", err)
	}
	// The shipped definition is back, so nothing was orphaned.
	src, err := s.GetSource(ctx, "lcgft")
	if err != nil || !src.Builtin {
		t.Fatalf("shipped definition did not return: %+v err=%v", src, err)
	}
	views, err := s.Views(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		if v.Name == "lcgft" && v.Orphan {
			t.Error("dropping a builtin override orphaned its install")
		}
	}
}

// TestViewsMarkOrphanInstalls gives the client the one fact it cannot derive: this
// row has no source record behind it, so Upload and Delete would 404 (tasks/255).
// An empty SnapshotURL is not a proxy -- an upload-only source has none either.
func TestViewsMarkOrphanInstalls(t *testing.T) {
	s := newService(t)
	ctx := t.Context()
	ix, err := vocab.Load(ctx, s.Blob, s.AuthoritiesPrefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Index = ix
	// An upload-only source: registered, no snapshot URL. The control that stops
	// "no snapshotUrl" from passing as an orphan test.
	if err := s.PutSource(ctx, Source{Name: "zzupload", Scheme: "zzupload"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallUpload(ctx, "zzupload", strings.NewReader(zzleakNT)); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, Source{Name: "zzgone", Scheme: "zzgone"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallUpload(ctx, "zzgone", strings.NewReader(zinesNT)); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.Delete(ctx, store.Record{Key: sourceKey("zzgone")}, store.CondNone); err != nil {
		t.Fatal(err)
	}

	views, err := s.Views(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, v := range views {
		seen[v.Name] = true
		switch v.Name {
		case "zzgone":
			if !v.Orphan {
				t.Error("an install with no source record is not marked orphan")
			}
		case "zzupload":
			if v.Orphan {
				t.Error("an upload-only source is marked orphan; it has a source record and no snapshot URL")
			}
		}
	}
	if !seen["zzgone"] || !seen["zzupload"] {
		t.Fatalf("views missing rows: %v", seen)
	}
}

// TestInstallUpload is the hand-supplied dump path: same converter and index
// swap as a download, no snapshot URL required, provenance recorded as
// "upload"; junk and unknown sources are refused.
func TestInstallUpload(t *testing.T) {
	s := newService(t)
	ctx := t.Context()
	ix, err := vocab.Load(ctx, s.Blob, s.AuthoritiesPrefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Index = ix
	if err := s.PutSource(ctx, Source{Name: "lcgft", Scheme: "lcgft"}); err != nil {
		t.Fatal(err)
	}
	terms, err := s.InstallUpload(ctx, "lcgft", strings.NewReader(zinesNT))
	if err != nil || terms != 2 {
		t.Fatalf("InstallUpload: terms=%d err=%v", terms, err)
	}
	if hits := ix.Search("lcgft", "zin", 5); len(hits) != 1 {
		t.Fatalf("search after upload: %+v", hits)
	}
	installed, err := s.Installed(ctx)
	if err != nil || len(installed) != 1 || installed[0].SnapshotURL != "upload" || installed[0].Terms != 2 {
		t.Fatalf("installed = %+v err=%v", installed, err)
	}
	if _, err := s.InstallUpload(ctx, "lcgft", strings.NewReader("not rdf\n")); !errors.Is(err, ErrValidation) {
		t.Fatalf("junk upload err = %v", err)
	}
	if _, err := s.InstallUpload(ctx, "nope", strings.NewReader(zinesNT)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown source err = %v", err)
	}
	// Wrong formats are named outright: zip archives and XML exports.
	_, err = s.InstallUpload(ctx, "lcgft", strings.NewReader("PK\x03\x04zipbytes"))
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "zip archive") {
		t.Fatalf("zip upload err = %v", err)
	}
	_, err = s.InstallUpload(ctx, "lcgft", strings.NewReader(`<?xml version="1.0"?><rdf:RDF></rdf:RDF>`))
	if !errors.Is(err, ErrValidation) || !strings.Contains(err.Error(), "XML") {
		t.Fatalf("xml upload err = %v", err)
	}
}

func TestDownloadFailureLandsInJob(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	s := newService(t)
	s.HTTPClient = srv.Client()
	ctx := t.Context()
	if err := s.PutSource(ctx, Source{Name: "bad", Scheme: "bad", SnapshotURL: srv.URL + "/gone.nt"}); err != nil {
		t.Fatal(err)
	}
	job, err := s.CreateDownload(ctx, "eve@example.com", "bad")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunDownload(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	job, _ = s.GetJob(ctx, job.ID)
	if job.Status != StatusFailed || job.Error == "" {
		t.Fatalf("want FAILED with error, got %+v", job)
	}
	// A live-only source refuses download.
	if _, err := s.CreateDownload(ctx, "eve@example.com", "lcnaf"); !errors.Is(err, ErrValidation) {
		t.Fatalf("lcnaf download: %v", err)
	}
}

func TestSchemesUnion(t *testing.T) {
	s := newService(t)
	ctx := t.Context()
	// No base filter: nil means "everything", installs need no bookkeeping.
	if schemes, err := s.Schemes(ctx); err != nil || schemes != nil {
		t.Fatalf("want nil schemes, got %v err=%v", schemes, err)
	}
	s.BaseSchemes = []string{"local", "homosaurus"}
	out, _, err := Convert(strings.NewReader(zinesNT), "lcgft")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Blob.Put(ctx, s.snapshotPath("lcgft"), out, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Blob.Put(ctx, s.metaPath("lcgft"),
		[]byte(`{"source":"lcgft","scheme":"lcgft","terms":2}`), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	schemes, err := s.Schemes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"local", "homosaurus", "lcgft"}
	if len(schemes) != 3 || schemes[0] != want[0] || schemes[2] != want[2] {
		t.Fatalf("schemes = %v, want %v", schemes, want)
	}
}

func TestEnricherReconciles(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Query().Get("q") {
		case "necromancy":
			_, _ = w.Write([]byte(`{"hits":[{"uri":"http://id.loc.gov/authorities/subjects/sh85090542","aLabel":"Necromancy in literature"}]}`))
		case "zines":
			_, _ = w.Write([]byte(`{"hits":[{"uri":"http://id.loc.gov/authorities/subjects/shX","aLabel":"Zines"}]}`))
		default:
			_, _ = w.Write([]byte(`{"hits":[]}`))
		}
	}))
	defer srv.Close()
	src := Source{
		Name: "lcsh", Scheme: "lcsh",
		SuggestFlavor: FlavorSuggest2, SuggestURL: srv.URL, SuggestDataset: "authorities/subjects",
	}
	e := NewEnricher(src, &SuggestClient{Client: srv.Client()})
	var _ ingest.Enricher = e
	works := []ingest.WorkSummary{
		{WorkID: "w1", Tags: []string{"Zines", "Necromancy"}},
		{WorkID: "w2", Tags: []string{"zines", "unknown thing"}},
	}
	out, err := e.Enrich(context.Background(), works)
	if err != nil {
		t.Fatal(err)
	}
	// Exact match "zines" lands on both works; prefix-only "necromancy"
	// (0.6) stays below the 0.9 default.
	if len(out) != 2 || len(out[0].Subjects) != 1 || out[0].Subjects[0].Labels["en"] != "Zines" {
		t.Fatalf("enrichments: %+v", out)
	}
	if calls != 3 {
		t.Errorf("cache miss: %d calls, want 3 (zines, necromancy, unknown thing)", calls)
	}
}

// TestEnricherIndexUpgradesTerms covers the local-index arm (tasks/178): a
// suggest match whose scheme is installed picks up the full term description
// (multilingual labels, broader edges) and its skos:broader ancestor chain
// rides along as Enrichment.Terms.
func TestEnricherIndexUpgradesTerms(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") == "zines" {
			_, _ = w.Write([]byte(`{"hits":[{"uri":"http://id.loc.gov/authorities/subjects/shX","aLabel":"Zines"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"hits":[]}`))
	}))
	defer srv.Close()
	const lcshNT = `<http://id.loc.gov/authorities/subjects/shX> <http://www.w3.org/2004/02/skos/core#prefLabel> "Zines"@en <authority:lcsh> .
<http://id.loc.gov/authorities/subjects/shX> <http://www.w3.org/2004/02/skos/core#prefLabel> "Fanzines"@es <authority:lcsh> .
<http://id.loc.gov/authorities/subjects/shX> <http://www.w3.org/2004/02/skos/core#broader> <http://id.loc.gov/authorities/subjects/shP> <authority:lcsh> .
<http://id.loc.gov/authorities/subjects/shP> <http://www.w3.org/2004/02/skos/core#prefLabel> "Periodicals"@en <authority:lcsh> .
`
	bs := blob.NewMem()
	if _, err := bs.Put(t.Context(), "data/authorities/lcsh.nq", []byte(lcshNT), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := vocab.Load(t.Context(), bs, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	src := Source{
		Name: "lcsh", Scheme: "lcsh",
		SuggestFlavor: FlavorSuggest2, SuggestURL: srv.URL, SuggestDataset: "authorities/subjects",
	}
	e := NewEnricher(src, &SuggestClient{Client: srv.Client()})
	e.Index = ix
	out, err := e.Enrich(context.Background(), []ingest.WorkSummary{{WorkID: "w1", Tags: []string{"Zines"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].Subjects) != 1 {
		t.Fatalf("enrichments: %+v", out)
	}
	subj := out[0].Subjects[0]
	if subj.Labels["es"] != "Fanzines" || len(subj.Broader) != 1 || subj.Broader[0] != "http://id.loc.gov/authorities/subjects/shP" {
		t.Fatalf("index-upgraded subject = %+v", subj)
	}
	if len(out[0].Terms) != 1 || out[0].Terms[0].URI != "http://id.loc.gov/authorities/subjects/shP" ||
		out[0].Terms[0].Labels["en"] != "Periodicals" {
		t.Fatalf("ancestor terms = %+v", out[0].Terms)
	}
}

// syntheticDump yields n distinct SKOS prefLabel lines without materializing
// them -- the memory-bound test's input.
type syntheticDump struct {
	n, emitted int
	rest       []byte
}

func (s *syntheticDump) Read(p []byte) (int, error) {
	total := 0
	for {
		if len(s.rest) == 0 {
			if s.emitted >= s.n {
				if total == 0 {
					return 0, io.EOF
				}
				return total, nil
			}
			s.rest = fmt.Appendf(nil,
				`<http://example.org/concept/%d> <http://www.w3.org/2004/02/skos/core#prefLabel> "Concept number %d with a reasonably long label to bulk the dump up"@en .`+"\n",
				s.emitted, s.emitted)
			s.emitted++
		}
		k := copy(p[total:], s.rest)
		s.rest = s.rest[k:]
		total += k
		if total == len(p) {
			return total, nil
		}
	}
}

// TestConvertToStreamsWithBoundedMemory pins the tasks/110 acceptance: a dump far
// larger than memory converts with heap growth bounded by the concept set, not by
// the dump or the output. Since tasks/320 the converter decodes one statement at a
// time, so the concept set is the only thing left that scales with the input.
func TestConvertToStreamsWithBoundedMemory(t *testing.T) {
	const n = 400_000 // ~60MB of input, ~60MB of output
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var written int64
	terms, err := ConvertTo(countWriter{&written}, &syntheticDump{n: n}, "lcsh", 0)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if terms != n {
		t.Fatalf("terms = %d, want %d", terms, n)
	}
	if written < 50<<20 {
		t.Fatalf("output only %d bytes -- synthetic dump too small to prove anything", written)
	}
	// The concepts set legitimately holds n URIs (~30MB here); everything else is
	// one statement at a time. Half the output size is a generous line, well under
	// the old whole-dump buffer.
	if grew := int64(after.HeapAlloc) - int64(before.HeapAlloc); grew > written/2 {
		t.Fatalf("heap grew %d bytes for %d bytes of output -- converter is buffering the dump", grew, written)
	}
}

type countWriter struct{ n *int64 }

func (w countWriter) Write(p []byte) (int, error) {
	*w.n += int64(len(p))
	return len(p), nil
}

// TestConvertToCaps pins the defensive ceilings: an over-cap dump and a
// newline-less body both fail cleanly instead of growing without bound.
//
// Each ceiling cuts the reader off mid-line, so the decoder's own account of the
// truncated tail is a SyntaxError blaming the dump. ConvertTo has to report the
// ceiling that caused the truncation instead, and matching the sentinel rather than
// the message is what pins that ordering.
func TestConvertToCaps(t *testing.T) {
	if _, err := ConvertTo(io.Discard, &syntheticDump{n: 100_000}, "lcsh", 1<<20); !errors.Is(err, errDumpTooLarge) {
		t.Fatalf("over-cap dump: err = %v, want errDumpTooLarge", err)
	}
	noNewline := strings.NewReader(strings.Repeat("x", maxDumpLine+2<<20))
	if _, err := ConvertTo(io.Discard, noNewline, "lcsh", 0); !errors.Is(err, errLineTooLong) {
		t.Fatalf("newline-less dump: err = %v, want errLineTooLong", err)
	}
}

// TestInstallUploadKeepsOldSnapshotOnBadDump pins the pipe abort: a failed
// conversion must not clobber (or leave a partial) snapshot.
func TestInstallUploadKeepsOldSnapshotOnBadDump(t *testing.T) {
	s := newService(t)
	ctx := t.Context()
	ix, err := vocab.Load(ctx, s.Blob, s.AuthoritiesPrefix, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Index = ix
	if err := s.PutSource(ctx, Source{Name: "lcgft", Scheme: "lcgft"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallUpload(ctx, "lcgft", strings.NewReader(zinesNT)); err != nil {
		t.Fatal(err)
	}
	want, _, err := s.Blob.Get(ctx, s.snapshotPath("lcgft"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InstallUpload(ctx, "lcgft", strings.NewReader("<?xml version=\"1.0\"?><rdf/>")); !errors.Is(err, ErrValidation) {
		t.Fatalf("xml dump: err = %v, want ErrValidation", err)
	}
	got, _, err := s.Blob.Get(ctx, s.snapshotPath("lcgft"))
	if err != nil || string(got) != string(want) {
		t.Fatalf("snapshot changed after failed install (err %v)", err)
	}
}

// TestFastURI covers the idroot -> canonical URI mapping (tasks/132): fst
// prefix and zero padding stripped; a malformed idroot yields no URI (the hit
// is skipped rather than emitting a bogus identifier).
func TestFastURI(t *testing.T) {
	cases := map[string]string{
		"fst01108566": "http://id.worldcat.org/fast/1108566",
		"fst00000042": "http://id.worldcat.org/fast/42",
		"1108566":     "http://id.worldcat.org/fast/1108566",
		"fst":         "",
		"fstX9":       "",
		"":            "",
	}
	for in, want := range cases {
		if got := fastURI(in); got != want {
			t.Errorf("fastURI(%q) = %q, want %q", in, got, want)
		}
	}
}
