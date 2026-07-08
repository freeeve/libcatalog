package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/project"
)

// rebuildGrain renders a minimal projectable grain: one minted Work with a
// title, optionally merged into another work (the redirect marker).
func rebuildGrain(id, title, mergedInto string) []byte {
	g := fmt.Appendf(nil, `<#%[1]sWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#%[1]sWork> <http://id.loc.gov/ontologies/bibframe/title> _:t <feed:overdrive> .
_:t <http://id.loc.gov/ontologies/bibframe/mainTitle> "%[2]s" <feed:overdrive> .
`, id, title)
	if mergedInto != "" {
		g = fmt.Appendf(g, `<#%sWork> <https://github.com/freeeve/libcat/ns#mergedInto> <#%sWork> <editorial:> .
`, id, mergedInto)
	}
	return g
}

// writeGrain writes a grain into the store at its canonical path and returns
// that path.
func writeGrain(t *testing.T, store, id string, g []byte) string {
	t.Helper()
	p := bibframe.GrainPath(id)
	full := filepath.Join(store, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, g, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// writeFeed writes a change feed carrying the given paths at the given epoch.
func writeFeed(t *testing.T, store string, epoch uint64, paths ...string) {
	t.Helper()
	type rec struct {
		Path string `json:"path"`
	}
	recs := make([]rec, len(paths))
	for i, p := range paths {
		recs[i] = rec{Path: p}
	}
	b, err := json.Marshal(map[string]any{"version": 1, "epoch": epoch, "records": recs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "data", "workindex.feed"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readCatalog(t *testing.T, out string) project.Catalog {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cat project.Catalog
	if err := json.Unmarshal(b, &cat); err != nil {
		t.Fatal(err)
	}
	return cat
}

func titles(cat project.Catalog) map[string]string {
	m := map[string]string{}
	for _, w := range cat.Works {
		m[w.ID] = w.Title
	}
	return m
}

// TestRebuildIncremental: the first run (no cursor) is a full rebuild; a
// subsequent run re-projects only the grains the feed names -- an edit, a
// delete, an add, and a merge redirect all land, and untouched works survive.
func TestRebuildIncremental(t *testing.T) {
	store, out := t.TempDir(), t.TempDir()
	p1 := writeGrain(t, store, "w1", rebuildGrain("w1", "First", ""))
	writeGrain(t, store, "w2", rebuildGrain("w2", "Second", ""))
	writeFeed(t, store, 0, p1)

	run := func(extra ...string) error {
		return runRebuild(append([]string{"--store", store, "--out", out}, extra...))
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	cat := readCatalog(t, out)
	if got := titles(cat); len(cat.Works) != 2 || got["w1"] != "First" || got["w2"] != "Second" {
		t.Fatalf("after full rebuild: %v", got)
	}
	for _, f := range []string{"facets.json", "redirects.json", "rebuild-cursor.json", "browse-index.rrs", "browse-records.bin", "browse-docs.json"} {
		if _, err := os.Stat(filepath.Join(out, f)); err != nil {
			t.Fatalf("missing artifact %s: %v", f, err)
		}
	}

	// Mutate: retitle w1, delete w2, add w3 merged into w1.
	p1 = writeGrain(t, store, "w1", rebuildGrain("w1", "First Revised", ""))
	p2 := bibframe.GrainPath("w2")
	if err := os.Remove(filepath.Join(store, filepath.FromSlash(p2))); err != nil {
		t.Fatal(err)
	}
	p3 := writeGrain(t, store, "w3", rebuildGrain("w3", "Third", "w1"))
	writeFeed(t, store, 0, p1, p1, p2, p3) // duplicate record exercises dedup
	if err := run(); err != nil {
		t.Fatal(err)
	}
	cat = readCatalog(t, out)
	got := titles(cat)
	if got["w1"] != "First Revised" || got["w3"] != "Third" {
		t.Fatalf("after incremental: %v", got)
	}
	if _, ok := got["w2"]; ok {
		t.Fatal("deleted w2 still projected")
	}
	var rm project.RedirectMap
	b, err := os.ReadFile(filepath.Join(out, "redirects.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rm); err != nil {
		t.Fatal(err)
	}
	if len(rm.Redirects) != 1 || rm.Redirects[0].From != "w3" || rm.Redirects[0].To != "w1" {
		t.Fatalf("redirects after incremental = %+v", rm.Redirects)
	}

	// Unchanged feed: a re-run is a no-op, not a rebuild.
	if err := run(); err != nil {
		t.Fatal(err)
	}

	// A fold (epoch advance) forces the full-chain fallback and still lands
	// the current corpus state.
	writeGrain(t, store, "w1", rebuildGrain("w1", "First Folded", ""))
	writeFeed(t, store, 1)
	if err := run(); err != nil {
		t.Fatal(err)
	}
	cat = readCatalog(t, out)
	if got := titles(cat); got["w1"] != "First Folded" || len(cat.Works) != 2 {
		t.Fatalf("after fold full rebuild: %v", got)
	}
}

// TestRebuildSchemaBumpForcesFull: a cursor written under a different artifact
// schema is not trusted for a delta.
func TestRebuildSchemaBumpForcesFull(t *testing.T) {
	store, out := t.TempDir(), t.TempDir()
	p1 := writeGrain(t, store, "w1", rebuildGrain("w1", "First", ""))
	writeFeed(t, store, 0, p1)
	if err := runRebuild([]string{"--store", store, "--out", out}); err != nil {
		t.Fatal(err)
	}
	cur, err := readRebuildCursor(filepath.Join(out, "rebuild-cursor.json"))
	if err != nil {
		t.Fatal(err)
	}
	cur.ProjectSchema--
	if err := writeJSON(filepath.Join(out, "rebuild-cursor.json"), cur); err != nil {
		t.Fatal(err)
	}
	if _, reason := incrementalDelta(nil, rebuildFeed{Epoch: cur.Epoch}, filepath.Join(out, "rebuild-cursor.json")); reason == "" {
		t.Fatal("stale-schema cursor admitted an incremental run")
	}
}
