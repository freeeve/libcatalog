package vocabsrc

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/freeeve/libcatalog/ingest"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/vocab"
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
	for _, name := range []string{"lcsh", "lcgft", "lcshac", "lcnaf", "wikidata", "viaf"} {
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
	// Malformed lines are skipped, not fatal.
	_, terms, err = Convert(strings.NewReader("not rdf at all\n"+zinesNT), "lcgft")
	if err != nil || terms != 2 {
		t.Fatalf("lenient parse: err=%v terms=%d", err, terms)
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
	// Remove: snapshot and sidecar go, terms drop out of the index.
	if err := s.RemoveSnapshot(ctx, "lcgft"); err != nil {
		t.Fatal(err)
	}
	if got := ix.Search("lcgft", "zin", 5); len(got) != 0 {
		t.Fatalf("terms survive removal: %+v", got)
	}
	if err := s.RemoveSnapshot(ctx, "lcgft"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("double remove: %v", err)
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
