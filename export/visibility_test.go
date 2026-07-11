// `lcat project` drops a suppressed or tombstoned Work before it
// reaches catalog.json, so the OPAC, the facets, the search index and the
// "more like this" rail all forget it. The exporter published from the store
// instead of from the graph view, so the same Work kept its cover at a
// derivable URL, its complete RDF in catalog.nq.gz, and its MARC record in
// catalog.mrc.gz / catalog.xml.gz.
//
// Suppression is the takedown button. These tests hold the download path to the
// stance the projector already honours, and -- because a leak is an absence of a
// filter, and an absence is what a broken test also reports -- every one of them
// carries a visible control alongside the hidden subject.
package export

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"
)

// hideGrain applies a visibility stance to a Work's grain in place, the way
// POST /v1/works/{id}/visibility does.
func hideGrain(t *testing.T, root, workID string, mutate func([]byte, string) ([]byte, error)) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(bibframe.GrainPath(workID)))
	grain, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := mutate(grain, workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// workIDs recovers the Work ids in a grain root, in path order.
func workIDs(t *testing.T, root string) []string {
	t.Helper()
	paths, err := grainPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(paths))
	for _, p := range paths {
		ids = append(ids, strings.TrimSuffix(filepath.Base(p), ".nq"))
	}
	return ids
}

// sentinels builds a three-Work corpus and hides two of them: one suppressed,
// one tombstoned. It returns the root and the three ids (visible, suppressed,
// tombstoned), each with a cover blob planted the way PUT /cover does.
func sentinels(t *testing.T) (root, visible, suppressed, tombstoned string) {
	t.Helper()
	root = corpus(t,
		bookRecord("c1", "9780000000011", "Visible, V.", "A Public Book"),
		bookRecord("c2", "9780000000028", "Hidden, S.", "A Suppressed Book"),
		bookRecord("c3", "9780000000035", "Hidden, T.", "A Tombstoned Book"),
	)
	ids := workIDs(t, root)
	if len(ids) != 3 {
		t.Fatalf("want 3 grains, got %d: %v", len(ids), ids)
	}
	// Title order is not path order; find each Work by the title in its grain.
	byTitle := map[string]string{}
	for _, id := range ids {
		grain, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(bibframe.GrainPath(id))))
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.Contains(string(grain), "A Public Book"):
			byTitle["visible"] = id
		case strings.Contains(string(grain), "A Suppressed Book"):
			byTitle["suppressed"] = id
		case strings.Contains(string(grain), "A Tombstoned Book"):
			byTitle["tombstoned"] = id
		}
	}
	visible, suppressed, tombstoned = byTitle["visible"], byTitle["suppressed"], byTitle["tombstoned"]
	if visible == "" || suppressed == "" || tombstoned == "" {
		t.Fatalf("could not identify the sentinels: %v", byTitle)
	}

	for _, id := range []string{visible, suppressed, tombstoned} {
		plantCover(t, root, id)
	}
	hideGrain(t, root, suppressed, func(g []byte, id string) ([]byte, error) {
		return bibframe.SetSuppressed(g, id, true)
	})
	hideGrain(t, root, tombstoned, func(g []byte, id string) ([]byte, error) {
		return bibframe.SetTombstone(g, id, "") // no successor: the "gone" stance
	})
	return root, visible, suppressed, tombstoned
}

// plantCover writes a cover blob and the grain statement that claims it, the
// way PUT /v1/works/{id}/cover does.
//
// Through bibframe.CoverBlobPath, deliberately: the blob tree is **sharded**
// (data/covers/<xx>/<id>.<ext>) and a fixture that writes the flat path agrees
// with a reader that reads the flat path, so both can be wrong together. That is
// exactly what happened -- and the positive control here passed
// while the real exporter published nothing.
func plantCover(t *testing.T, root, workID string) {
	t.Helper()
	blobPath := filepath.Join(root, filepath.FromSlash(bibframe.CoverBlobPath(workID, "png")))
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blobPath, []byte("PNG"+workID), 0o644); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, filepath.FromSlash(bibframe.GrainPath(workID)))
	grain, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	out, err := bibframe.SetCover(grain, workID, "covers/"+workID+".png")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A hidden Work's complete RDF must not reach the nq download. The visible
// Work's must, or the filter is simply emitting nothing.
func TestNQDownloadOmitsHiddenWorks(t *testing.T) {
	root, visible, suppressed, tombstoned := sentinels(t)
	out := t.TempDir()
	if _, err := Run(Options{In: root, Out: out, CoversOut: t.TempDir(), Log: io.Discard}); err != nil {
		t.Fatal(err)
	}
	nq := gunzip(t, filepath.Join(out, "catalog.nq.gz"))

	if !strings.Contains(nq, visible) {
		t.Error("the visible Work is missing from catalog.nq.gz -- the filter drops everything")
	}
	for name, id := range map[string]string{"suppressed": suppressed, "tombstoned": tombstoned} {
		if n := strings.Count(nq, id); n > 0 {
			t.Errorf("catalog.nq.gz names the %s Work %q in %d places", name, id, n)
		}
	}
}

// Dropping the hidden Work's own quads is not enough. A visible Work's grain can
// name it -- `bf:hasPart <#wHiddenWork>` -- and that quad lives in the *visible*
// grain, so it survives the record filter and publishes both the hidden id and a
// statement about it. The projector already strips these (resolveRelations drops
// links whose target left the projection); the exporter must too.
//
// Found on the playground store, not by reading the code: a suppressed work was
// gone as a subject and still named once as an object.
func TestNQDownloadDropsLinksIntoHiddenWorks(t *testing.T) {
	root, visible, suppressed, tombstoned := sentinels(t)
	// The visible Work asserts a whole/part relation to each hidden one.
	p := filepath.Join(root, filepath.FromSlash(bibframe.GrainPath(visible)))
	grain, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	link := func(id string) string {
		return "<" + bibframe.WorkIRI(visible) + "> <http://id.loc.gov/ontologies/bibframe/hasPart> <" +
			bibframe.WorkIRI(id) + "> <editorial:> .\n"
	}
	grain = append(grain, []byte(link(suppressed)+link(tombstoned))...)
	if err := os.WriteFile(p, grain, 0o644); err != nil {
		t.Fatal(err)
	}

	out := t.TempDir()
	if _, err := Run(Options{In: root, Out: out, CoversOut: t.TempDir(), Log: io.Discard}); err != nil {
		t.Fatal(err)
	}
	nq := gunzip(t, filepath.Join(out, "catalog.nq.gz"))
	if !strings.Contains(nq, bibframe.WorkIRI(visible)) {
		t.Fatal("the visible Work left the download entirely -- the filter is too broad")
	}
	for name, id := range map[string]string{"suppressed": suppressed, "tombstoned": tombstoned} {
		if strings.Contains(nq, id) {
			t.Errorf("a visible Work's grain still names the %s Work %q in the download", name, id)
		}
	}
	// The visible Work keeps every quad that does not point into hidden territory.
	if !strings.Contains(nq, "A Public Book") {
		t.Error("the visible Work lost its own statements")
	}
	// The MARC crosswalk does not carry these links today. Assert it, so that if it
	// ever learns to emit 76X-78X linking entries they arrive filtered.
	for _, f := range []string{"catalog.mrc.gz", "catalog.xml.gz"} {
		s := gunzip(t, filepath.Join(out, f))
		for name, id := range map[string]string{"suppressed": suppressed, "tombstoned": tombstoned} {
			if strings.Contains(s, id) {
				t.Errorf("%s names the %s Work id %q", f, name, id)
			}
		}
	}
}

// A hidden Work's MARC record must not reach the download. Counting records is
// not enough: a wrong record could be dropped.
func TestMARCDownloadOmitsHiddenWorks(t *testing.T) {
	root, _, _, _ := sentinels(t)
	out := t.TempDir()
	m, err := Run(Options{In: root, Out: out, CoversOut: t.TempDir(), Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	mrc := gunzip(t, filepath.Join(out, "catalog.mrc.gz"))
	xml := gunzip(t, filepath.Join(out, "catalog.xml.gz"))

	if !strings.Contains(mrc, "A Public Book") || !strings.Contains(xml, "A Public Book") {
		t.Fatal("the visible Work is missing from the MARC downloads")
	}
	for _, title := range []string{"A Suppressed Book", "A Tombstoned Book"} {
		if strings.Contains(mrc, title) {
			t.Errorf("catalog.mrc.gz carries %q", title)
		}
		if strings.Contains(xml, title) {
			t.Errorf("catalog.xml.gz carries %q", title)
		}
	}
	if m.Works != 1 {
		t.Errorf("manifest reports %d works, want 1 -- the count must describe what shipped", m.Works)
	}
}

// The cover of a hidden Work must not be published. Its blob still exists in the
// store and its grain still claims it -- suppression is not deletion -- so only
// the exporter can decline to copy it.
func TestCoversPublishOnlyVisibleWorks(t *testing.T) {
	root, visible, suppressed, tombstoned := sentinels(t)
	coversOut := t.TempDir()
	if _, err := Run(Options{In: root, Out: t.TempDir(), CoversOut: coversOut, Log: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(coversOut, visible+".png")); err != nil {
		t.Fatalf("the visible Work's cover was not published: %v", err)
	}
	for name, id := range map[string]string{"suppressed": suppressed, "tombstoned": tombstoned} {
		if _, err := os.Stat(filepath.Join(coversOut, id+".png")); err == nil {
			t.Errorf("published the %s Work's cover at covers/%s.png", name, id)
		}
	}
}

// A blob no visible Work claims is not published either -- the
// stale-format residue, which `lcat covers --reap` collects from the store only
// after the fact. Driving the copy from the grains collects it for free.
func TestCoversSkipBlobsNoWorkClaims(t *testing.T) {
	root, visible, _, _ := sentinels(t)
	orphan := filepath.Join(root, filepath.FromSlash(bibframe.CoverBlobPath("worphan00000001", "jpg")))
	if err := os.MkdirAll(filepath.Dir(orphan), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("JPG"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stale format for a Work that exists: the grain claims .png, not .webp.
	stale := filepath.Join(root, filepath.FromSlash(bibframe.CoverBlobPath(visible, "webp")))
	if err := os.WriteFile(stale, []byte("WEBP"), 0o644); err != nil {
		t.Fatal(err)
	}
	coversOut := t.TempDir()
	if _, err := Run(Options{In: root, Out: t.TempDir(), CoversOut: coversOut, Log: io.Discard}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(coversOut, visible+".png")); err != nil {
		t.Fatalf("the claimed cover was not published: %v", err)
	}
	for _, name := range []string{"worphan00000001.jpg", visible + ".webp"} {
		if _, err := os.Stat(filepath.Join(coversOut, name)); err == nil {
			t.Errorf("published %s, which no visible Work claims", name)
		}
	}
}

// the cover blob tree is sharded and the site serves it flat. The
// exporter constructed its read path without the shard, missed every blob, and
// `os.IsNotExist -> continue` published zero covers in silence.
//
// This asserts the shard on both sides at once: the fixture writes only where
// CoverBlobPath says, and the test fails if the flat path exists at all -- so a
// reader that reads flat cannot be rescued by a fixture that writes flat.
func TestCoversAreReadFromTheShardedBlobPath(t *testing.T) {
	root, visible, _, _ := sentinels(t)
	flat := filepath.Join(root, "data", "covers", visible+".png")
	if _, err := os.Stat(flat); err == nil {
		t.Fatalf("the fixture wrote the flat path %s; it must write only the sharded one", flat)
	}
	sharded := filepath.Join(root, filepath.FromSlash(bibframe.CoverBlobPath(visible, "png")))
	if _, err := os.Stat(sharded); err != nil {
		t.Fatalf("the fixture did not write the sharded blob: %v", err)
	}

	coversOut := t.TempDir()
	var log strings.Builder
	if _, err := Run(Options{In: root, Out: t.TempDir(), CoversOut: coversOut, Log: &log}); err != nil {
		t.Fatal(err)
	}
	// Published flat, under the base name the site-relative claim carries.
	data, err := os.ReadFile(filepath.Join(coversOut, visible+".png"))
	if err != nil {
		t.Fatalf("the visible Work's cover was not published: %v", err)
	}
	if string(data) != "PNG"+visible {
		t.Errorf("published the wrong bytes: %q", data)
	}
	if strings.Contains(log.String(), "not in the store") {
		t.Errorf("a cover that is in the store was reported missing: %q", log.String())
	}
}

// A claimed cover the store does not hold is benign, but it must be counted and
// said out loud: "every cover is missing" and "one cover is missing" looked
// identical from the build log, which is how stayed invisible.
func TestMissingCoverIsReportedRatherThanSwallowed(t *testing.T) {
	root, visible, _, _ := sentinels(t)
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(bibframe.CoverBlobPath(visible, "png")))); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	coversOut := t.TempDir()
	if _, err := Run(Options{In: root, Out: t.TempDir(), CoversOut: coversOut, Log: &log}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(log.String(), "1 of 1 claimed covers are not in the store") {
		t.Errorf("the build log does not report the missing cover: %q", log.String())
	}
	entries, err := os.ReadDir(coversOut)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("published %d covers though the only claimed blob is gone", len(entries))
	}
}

// The manifest must carry a number that can disagree with the catalog. Works
// counted every grain, hidden or not, so a build that published 3274 records for
// a 31-work catalog reported 3274 and looked healthy.
func TestManifestCountsWhatShippedAndWhatItHeldBack(t *testing.T) {
	root, _, _, _ := sentinels(t)
	var log strings.Builder
	m, err := Run(Options{In: root, Out: t.TempDir(), CoversOut: t.TempDir(), Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	if m.Works != 1 || m.Hidden != 2 {
		t.Errorf("works=%d hidden=%d, want 1 and 2", m.Works, m.Hidden)
	}
	for _, f := range m.Files {
		if f.Name == "catalog.nq.gz" && f.Records != 1 {
			t.Errorf("catalog.nq.gz records=%d, want 1", f.Records)
		}
	}
	if !strings.Contains(log.String(), "2") || !strings.Contains(log.String(), "hidden") {
		t.Errorf("the build log never mentions the held-back works: %q", log.String())
	}
}

// The all-visible corpus must export byte-for-byte what a copy of catalog.nq
// exported. Without this, "no hidden work leaks" is satisfiable by shipping less
// than the catalog holds -- and the rebuild could silently reorder the corpus,
// which moves the download's sha256 for a catalog that did not change.
//
// The fixture must put the grains on disk in an order that is NOT their id order,
// or the sort in partitionByVisibility is unobserved and this check cannot see it.
// The guard below fails loudly if a future hash shard makes that true by accident.
func TestNothingIsHeldBackWhenNothingIsHidden(t *testing.T) {
	titles := []string{"First Book", "Second Book", "Third Book", "Fourth Book"}
	root := corpus(t,
		bookRecord("a1", "9780000000011", "Author, A.", titles[0]),
		bookRecord("b1", "9780000000028", "Author, B.", titles[1]),
		bookRecord("c1", "9780000000035", "Author, C.", titles[2]),
		bookRecord("d1", "9780000000042", "Author, D.", titles[3]),
	)
	onDiskOrder := workIDs(t, root)
	idOrder := append([]string(nil), onDiskOrder...)
	sort.Strings(idOrder)
	if slices.Equal(onDiskOrder, idOrder) {
		t.Fatalf("fixture no longer exercises grain reordering (%v): the byte-identity "+
			"check below cannot observe partitionByVisibility's sort", onDiskOrder)
	}

	out := t.TempDir()
	m, err := Run(Options{In: root, Out: out, Log: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if m.Works != len(titles) || m.Hidden != 0 {
		t.Fatalf("works=%d hidden=%d, want %d and 0", m.Works, m.Hidden, len(titles))
	}
	nq := gunzip(t, filepath.Join(out, "catalog.nq.gz"))
	onDisk, err := os.ReadFile(filepath.Join(root, "catalog.nq"))
	if err != nil {
		t.Fatal(err)
	}
	if nq != string(onDisk) {
		t.Errorf("the nq download is no longer the corpus written by serialize: "+
			"%d bytes exported, %d on disk", len(nq), len(onDisk))
	}
	mrc := gunzip(t, filepath.Join(out, "catalog.mrc.gz"))
	for _, title := range titles {
		if !strings.Contains(mrc, title) {
			t.Errorf("catalog.mrc.gz lost %q", title)
		}
	}
}
