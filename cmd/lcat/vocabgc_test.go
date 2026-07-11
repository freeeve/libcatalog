package main

import (
	"testing"

	"github.com/freeeve/libcat/storage/blob"
)

// The sidecar layout, as an operator sees it on disk. These paths are the stable
// public shape the command acts on; the vocab package tests the detection against
// real BuildSidecar output.
const gcPrefix = "data/authorities/"

func manifest(scheme, source string) []byte {
	return []byte(`{"version":2,"scheme":"` + scheme + `","source":"` + source + `","sourceETag":"e","sourceSchemes":["` + scheme + `"],"terms":1,"live":1}`)
}

// seedSidecar writes a manifest plus a couple of artifact files for scheme, naming
// source; seedSnapshot decides whether that source actually exists.
func seedSidecar(t *testing.T, bs blob.Store, scheme, source string, seedSnapshot bool) {
	t.Helper()
	ctx := t.Context()
	put := func(p string, b []byte) {
		if _, err := bs.Put(ctx, p, b, blob.PutOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	put(gcPrefix+"sidecar/"+scheme+".manifest.json", manifest(scheme, source))
	put(gcPrefix+"sidecar/"+scheme+".uri.rril", []byte("x"))
	put(gcPrefix+"sidecar/"+scheme+".search.rrt", []byte("x"))
	if seedSnapshot {
		put(source, []byte("<http://x> <http://y> <http://z> .\n"))
	}
}

func sidecarNames(t *testing.T, bs blob.Store) []string {
	t.Helper()
	var out []string
	for e, err := range bs.List(t.Context(), gcPrefix+"sidecar/") {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e.Path)
	}
	return out
}

// TestVocabGCReapsOrphansAndSparesLiveSidecars drives the command end to end over a
// real dir store: an orphan (its snapshot removed) is swept, a live sidecar is left
// intact.
func TestVocabGCReapsOrphansAndSparesLiveSidecars(t *testing.T) {
	dir := t.TempDir()
	bs := blob.NewDir(dir)
	seedSidecar(t, bs, "gone", gcPrefix+"vocab/gone.nq", false) // snapshot absent -> orphan
	seedSidecar(t, bs, "live", gcPrefix+"vocab/live.nq", true)  // snapshot present -> spared

	if err := runVocabGC([]string{"--store", dir, "--reap"}); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{}
	for _, p := range sidecarNames(t, bs) {
		got[p] = true
	}
	for _, p := range []string{"gone.manifest.json", "gone.uri.rril", "gone.search.rrt"} {
		if got[gcPrefix+"sidecar/"+p] {
			t.Errorf("orphan artifact survived the reap: %s", p)
		}
	}
	for _, p := range []string{"live.manifest.json", "live.uri.rril", "live.search.rrt"} {
		if !got[gcPrefix+"sidecar/"+p] {
			t.Errorf("live artifact was swept: %s", p)
		}
	}
}

// TestVocabGCReportsWithoutReaping is the safe default: no --reap touches nothing.
func TestVocabGCReportsWithoutReaping(t *testing.T) {
	dir := t.TempDir()
	bs := blob.NewDir(dir)
	seedSidecar(t, bs, "gone", gcPrefix+"vocab/gone.nq", false)
	before := len(sidecarNames(t, bs))

	if err := runVocabGC([]string{"--store", dir}); err != nil {
		t.Fatal(err)
	}
	if after := len(sidecarNames(t, bs)); after != before {
		t.Errorf("report-only run deleted files: %d -> %d", before, after)
	}
}

// TestVocabGCNeedsAStore keeps the flag contract honest.
func TestVocabGCNeedsAStore(t *testing.T) {
	if err := runVocabGC(nil); err == nil {
		t.Fatal("missing --store was accepted")
	}
}
