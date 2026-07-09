package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
)

// catalogWith writes a catalog.nq carrying one titled work under each named
// feed, and returns its path.
func catalogWith(t *testing.T, feeds ...string) string {
	t.Helper()
	ds := &rdf.Dataset{}
	for i, feed := range feeds {
		work := rdf.NewIRI(bibframe.WorkIRI("wfeed00000" + string(rune('a'+i))))
		ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"),
			rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/Work"), bibframe.FeedGraph(feed))
		ds.Add(work, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"),
			rdf.NewLiteral("A Book", "", ""), bibframe.FeedGraph(feed))
	}
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.nq")
	if err := os.WriteFile(path, nq, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// sentinelOut seeds an out dir with a catalog.json standing in for a populated,
// already-published site.
func sentinelOut(t *testing.T) (dir, catalog string) {
	t.Helper()
	dir = t.TempDir()
	catalog = filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catalog, []byte(`{"works":["the previously published site"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, catalog
}

// tasks/246, the reported repro: a catalog carrying only feed:marc, projected
// with the default --provider overdrive, used to write an empty catalog.json
// and exit 0. In LCATD_REBUILD_CMD that silently empties the discovery site.
func TestProjectRefusesToEmptyTheSiteOnAProviderTypo(t *testing.T) {
	nq := catalogWith(t, "marc")
	out, sentinel := sentinelOut(t)

	err := projectCatalog(nq, []string{"overdrive"}, nil, out, false)
	if err == nil {
		t.Fatal("projecting a marc-only catalog as overdrive succeeded; it must fail")
	}
	// The message has to tell the operator what the catalog does carry, or the
	// fix ("which provider, then?") is a guessing game.
	if !strings.Contains(err.Error(), "marc") {
		t.Errorf("error does not name the feed the catalog carries: %v", err)
	}
	if !strings.Contains(err.Error(), "overdrive") {
		t.Errorf("error does not name the provider that matched nothing: %v", err)
	}

	// And nothing was written: the published site is still there.
	got, rerr := os.ReadFile(sentinel)
	if rerr != nil {
		t.Fatalf("catalog.json was deleted: %v", rerr)
	}
	if !strings.Contains(string(got), "previously published") {
		t.Fatalf("catalog.json was overwritten with %q", got)
	}
}

// The same catalog with the right provider still projects.
func TestProjectSucceedsWithTheMatchingProvider(t *testing.T) {
	nq := catalogWith(t, "marc")
	out := t.TempDir()
	if err := projectCatalog(nq, []string{"marc"}, nil, out, false); err != nil {
		t.Fatalf("projecting marc as marc failed: %v", err)
	}
	for _, name := range []string{"catalog.json", "facets.json", "redirects.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s not written: %v", name, err)
		}
	}
}

// --allow-empty is the escape hatch for a deployment that genuinely projects
// nothing (a fresh store before its first ingest).
func TestAllowEmptyWritesTheEmptyCatalog(t *testing.T) {
	nq := catalogWith(t, "marc")
	out, sentinel := sentinelOut(t)
	if err := projectCatalog(nq, []string{"overdrive"}, nil, out, true); err != nil {
		t.Fatalf("--allow-empty still failed: %v", err)
	}
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "previously published") {
		t.Fatal("--allow-empty did not overwrite catalog.json")
	}
}

// A catalog with no feed graphs at all is a different failure, and says so:
// there is no provider that would have worked.
func TestProjectOnAFeedlessCatalogSaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.nq")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	err := projectCatalog(path, []string{"marc"}, nil, t.TempDir(), false)
	if err == nil {
		t.Fatal("an empty catalog projected successfully")
	}
	if !strings.Contains(err.Error(), "no feed graphs at all") {
		t.Errorf("error should distinguish an empty catalog: %v", err)
	}
	if !strings.Contains(err.Error(), "--allow-empty") {
		t.Errorf("error should point at the escape hatch: %v", err)
	}
}

// One good feed among several still projects; the typo'd one only warns, since
// the merge is first-wins and a missing feed contributes nothing.
func TestProjectWarnsButProceedsWhenOnlySomeFeedsAreMissing(t *testing.T) {
	nq := catalogWith(t, "marc")
	out := t.TempDir()
	if err := projectCatalog(nq, []string{"marc", "overdrve"}, nil, out, false); err != nil {
		t.Fatalf("a partially-matching provider list failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "catalog.json")); err != nil {
		t.Fatalf("catalog.json not written: %v", err)
	}
}

// Regression: an empty --provider keeps its original error, not the new one.
func TestNoProvidersKeepsItsOriginalError(t *testing.T) {
	nq := catalogWith(t, "marc")
	err := projectCatalog(nq, nil, nil, t.TempDir(), false)
	if err == nil || !strings.Contains(err.Error(), "no feeds to project") {
		t.Fatalf("want the original 'no feeds to project', got %v", err)
	}
}
