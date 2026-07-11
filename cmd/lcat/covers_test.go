package main

import (
	"context"
	"errors"
	"sort"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// seedCoverWork writes a Work grain, sets its cover statement (when cover is
// non-empty), and stores blobs for each extension given.
func seedCoverWork(t *testing.T, bs blob.Store, workID, cover string, exts ...string) {
	t.Helper()
	ctx := t.Context()
	ds := &rdf.Dataset{}
	ds.Add(rdf.NewIRI(bibframe.WorkIRI(workID)),
		rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"),
		rdf.NewLiteral("A Book", "", ""), bibframe.FeedGraph("overdrive"))
	grain, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if cover != "" {
		if grain, err = bibframe.SetCover(grain, workID, cover); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := bs.Put(ctx, bibframe.GrainPath(workID), grain, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	storeCoverBlobs(t, bs, workID, exts...)
}

func storeCoverBlobs(t *testing.T, bs blob.Store, workID string, exts ...string) {
	t.Helper()
	for _, ext := range exts {
		if _, err := bs.Put(t.Context(), bibframe.CoverBlobPath(workID, ext), []byte("bytes-"+ext), blob.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
}

func orphanPaths(orphans []orphanCover) []string {
	out := make([]string, 0, len(orphans))
	for _, o := range orphans {
		out = append(out, o.Path)
	}
	sort.Strings(out)
	return out
}

func reasonOf(t *testing.T, orphans []orphanCover, path string) orphanCover {
	t.Helper()
	for _, o := range orphans {
		if o.Path == path {
			return o
		}
	}
	t.Fatalf("%s is not an orphan; orphans = %v", path, orphanPaths(orphans))
	return orphanCover{}
}

// a cover replaced with a different format before v0.95.0 left the
// old image stored and publicly served, referenced by nothing.
func TestFindOrphanCoversDistinguishesItsReasons(t *testing.T) {
	bs := blob.NewMem()
	ctx := t.Context()

	// The residue: the grain says .png, the .jpg is still stored.
	seedCoverWork(t, bs, "wstale0000001", "covers/wstale0000001.png", "png", "jpg")
	// A work with a stored cover and no cover statement at all.
	seedCoverWork(t, bs, "wnocover00001", "", "png")
	// A blob whose work has no grain (tombstoned, or hand-deleted).
	storeCoverBlobs(t, bs, "wnograin00001", "webp")
	// A clean work: exactly the cover it references.
	seedCoverWork(t, bs, "wclean0000001", "covers/wclean0000001.jpg", "jpg")
	// Something that never came from CoverBlobPath.
	if _, err := bs.Put(ctx, "data/covers/README.txt", []byte("notes"), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	orphans, scanned, err := findOrphanCovers(ctx, bs)
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 6 {
		t.Fatalf("scanned = %d, want 6", scanned)
	}
	got := orphanPaths(orphans)
	want := []string{
		"data/covers/README.txt",
		bibframe.CoverBlobPath("wnocover00001", "png"),
		bibframe.CoverBlobPath("wnograin00001", "webp"),
		bibframe.CoverBlobPath("wstale0000001", "jpg"),
	}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("orphans = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("orphans = %v, want %v", got, want)
		}
	}

	// The referenced cover is never an orphan.
	for _, o := range orphans {
		if o.Path == bibframe.CoverBlobPath("wclean0000001", "jpg") || o.Path == bibframe.CoverBlobPath("wstale0000001", "png") {
			t.Fatalf("the referenced cover %s was reported", o.Path)
		}
	}

	stale := reasonOf(t, orphans, bibframe.CoverBlobPath("wstale0000001", "jpg"))
	if stale.Reason != reasonStaleFormat || stale.Referenced != "covers/wstale0000001.png" {
		t.Fatalf("stale = %+v", stale)
	}
	if r := reasonOf(t, orphans, bibframe.CoverBlobPath("wnocover00001", "png")).Reason; r != reasonNoCover {
		t.Fatalf("no-cover reason = %q", r)
	}
	if r := reasonOf(t, orphans, bibframe.CoverBlobPath("wnograin00001", "webp")).Reason; r != reasonNoWork {
		t.Fatalf("no-grain reason = %q", r)
	}
	if r := reasonOf(t, orphans, "data/covers/README.txt").Reason; r != reasonBadPath {
		t.Fatalf("bad-path reason = %q", r)
	}
}

// A work whose cover statement points at a provider URL references no local
// blob, so every blob stored for it is an orphan. This is the case a naive
// "does the work have a cover?" check would miss.
func TestFindOrphanCoversWithAnExternalCover(t *testing.T) {
	bs := blob.NewMem()
	seedCoverWork(t, bs, "wextern000001", "https://provider.example/art.jpg", "jpg", "png")
	orphans, _, err := findOrphanCovers(t.Context(), bs)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 2 {
		t.Fatalf("orphans = %v, want both local blobs", orphanPaths(orphans))
	}
	for _, o := range orphans {
		if o.Reason != reasonStaleFormat || o.Referenced != "https://provider.example/art.jpg" {
			t.Fatalf("orphan = %+v", o)
		}
	}
}

// An editorial cover overlays a feed one, so the local blob it names is kept
// even though the grain also carries a provider URL.
func TestFindOrphanCoversRespectsEditorialOverlay(t *testing.T) {
	bs := blob.NewMem()
	const workID = "woverlay00001"
	ds := &rdf.Dataset{}
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	feed := bibframe.FeedGraph("overdrive")
	ds.Add(work, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), rdf.NewLiteral("A Book", "", ""), feed)
	ds.Add(work, rdf.NewIRI(bibframe.ExtraPred+bibframe.CoverExtraKey), rdf.NewLiteral("https://provider.example/art.jpg", "", ""), feed)
	grain, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if grain, err = bibframe.SetCover(grain, workID, "covers/"+workID+".png"); err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(t.Context(), bibframe.GrainPath(workID), grain, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	storeCoverBlobs(t, bs, workID, "png")

	orphans, _, err := findOrphanCovers(t.Context(), bs)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Fatalf("the editorial cover was reported as an orphan: %v", orphanPaths(orphans))
	}
}

// An empty store is not an error, and neither is a store with no covers.
func TestFindOrphanCoversOnAnEmptyStore(t *testing.T) {
	orphans, scanned, err := findOrphanCovers(t.Context(), blob.NewMem())
	if err != nil {
		t.Fatal(err)
	}
	if scanned != 0 || len(orphans) != 0 {
		t.Fatalf("scanned %d, orphans %v", scanned, orphanPaths(orphans))
	}
}

// Reaping deletes exactly the orphans, leaves the referenced covers, and a
// second pass finds nothing.
func TestReapIsIdempotentAndSpares(t *testing.T) {
	dir := t.TempDir()
	bs := blob.NewDir(dir)
	seedCoverWork(t, bs, "wstale0000001", "covers/wstale0000001.png", "png", "jpg", "webp")
	seedCoverWork(t, bs, "wclean0000001", "covers/wclean0000001.jpg", "jpg")

	if err := runCovers([]string{"--store", dir, "--reap"}); err != nil {
		t.Fatalf("reap: %v", err)
	}
	ctx := context.Background()
	for _, ext := range []string{"jpg", "webp"} {
		if _, _, err := bs.Get(ctx, bibframe.CoverBlobPath("wstale0000001", ext)); !errors.Is(err, blob.ErrNotFound) {
			t.Fatalf("the stale .%s survived the reap", ext)
		}
	}
	for _, kept := range []string{bibframe.CoverBlobPath("wstale0000001", "png"), bibframe.CoverBlobPath("wclean0000001", "jpg")} {
		if _, _, err := bs.Get(ctx, kept); err != nil {
			t.Fatalf("the referenced cover %s was reaped: %v", kept, err)
		}
	}
	// The grains are untouched: this command never writes one.
	cover, err := coverOfStored(ctx, bs, "wstale0000001")
	if err != nil || cover != "covers/wstale0000001.png" {
		t.Fatalf("grain changed: cover=%q err=%v", cover, err)
	}

	orphans, scanned, err := findOrphanCovers(ctx, bs)
	if err != nil {
		t.Fatal(err)
	}
	if len(orphans) != 0 {
		t.Fatalf("a second pass still reports %v", orphanPaths(orphans))
	}
	if scanned != 2 {
		t.Fatalf("scanned = %d, want the 2 surviving covers", scanned)
	}
}

func coverOfStored(ctx context.Context, bs blob.Store, workID string) (string, error) {
	grain, _, err := bs.Get(ctx, bibframe.GrainPath(workID))
	if err != nil {
		return "", err
	}
	return bibframe.CoverOf(grain, workID)
}

// A dry run reports and deletes nothing -- the default, because this command
// deletes public images.
func TestCoversDefaultsToReportOnly(t *testing.T) {
	dir := t.TempDir()
	bs := blob.NewDir(dir)
	seedCoverWork(t, bs, "wstale0000001", "covers/wstale0000001.png", "png", "jpg")
	if err := runCovers([]string{"--store", dir}); err != nil {
		t.Fatalf("report: %v", err)
	}
	if _, _, err := bs.Get(t.Context(), bibframe.CoverBlobPath("wstale0000001", "jpg")); err != nil {
		t.Fatal("a report-only run deleted the orphan")
	}
}

func TestCoverWorkOf(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{bibframe.CoverBlobPath("wabc123def456", "png"), "wabc123def456"},
		{bibframe.CoverBlobPath("wabc123def456", "jpg"), "wabc123def456"},
		{bibframe.CoverBlobPath("wabc123def456", "webp"), "wabc123def456"},
		{"data/covers/wa/wabc123def456.gif", ""},     // not a cover format
		{"data/covers/wabc123def456.png", ""},        // unsharded
		{"data/covers/zz/wabc123def456.png", ""},     // shard disagrees with the id
		{"data/covers/wa/nonsense.png", ""},          // not a work id
		{"data/covers/README.txt", ""},               // not a cover at all
		{"data/works/wa/wabc123def456.nq", ""},       // not the cover tree
		{"data/covers/wa/sub/wabc123def456.png", ""}, // nested deeper than the shard
		{"data/covers/wa/wabc123def456.png.bak", ""}, // trailing extension
		{"data/covers/wA/wAbc123def456.png", ""},     // ids are lowercase
	}
	for _, tc := range cases {
		got, ok := coverWorkOf(tc.path)
		if (tc.want == "") == ok || got != tc.want {
			t.Errorf("coverWorkOf(%q) = (%q, %v), want %q", tc.path, got, ok, tc.want)
		}
	}
}
