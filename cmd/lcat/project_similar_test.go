package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcodex/rdf"
)

// twoRelatedWorks writes a catalog.nq with two Works sharing one subject and one
// Work sharing nothing, so the sidecar has both a rail and an omission to make.
func twoRelatedWorks(t *testing.T) string {
	t.Helper()
	ds := &rdf.Dataset{}
	add := func(id, title, subject string) {
		work := rdf.NewIRI(bibframe.WorkIRI(id))
		feed := bibframe.FeedGraph("marc")
		ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"),
			rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/Work"), feed)
		// bf:title points at a Title node carrying bf:mainTitle; a bare literal
		// projects as an empty title.
		titleNode := rdf.NewIRI("https://ex.org/title/" + id)
		ds.Add(work, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), titleNode, feed)
		ds.Add(titleNode, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/mainTitle"),
			rdf.NewLiteral(title, "", ""), feed)
		ds.Add(work, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/subject"),
			rdf.NewIRI(subject), feed)
	}
	add("wsimilar00a", "A Book", "https://ex.org/shared")
	add("wsimilar00b", "Another Book", "https://ex.org/shared")
	add("wsimilar00c", "A Lonely Book", "https://ex.org/nobody-else")

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

func readSidecar(t *testing.T, out string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(out, "similar.json"))
	if err != nil {
		t.Fatalf("similar.json: %v", err)
	}
	var idx map[string]any
	if err := json.Unmarshal(b, &idx); err != nil {
		t.Fatal(err)
	}
	return idx
}

// The sidecar is emitted alongside catalog.json, carries its own schema version,
// and omits Works with no neighbours rather than storing them empty.
func TestProjectWritesSimilarSidecar(t *testing.T) {
	out := t.TempDir()
	if err := projectCatalog(projectOptions{CatalogPath: twoRelatedWorks(t), Providers: []string{"marc"}, Out: out, SimilarLimit: DefaultSimilarLimit}); err != nil {
		t.Fatalf("projectCatalog: %v", err)
	}
	idx := readSidecar(t, out)

	if v, _ := idx["version"].(float64); int(v) != 1 {
		t.Errorf("sidecar version = %v, want 1", idx["version"])
	}
	if l, _ := idx["limit"].(float64); int(l) != DefaultSimilarLimit {
		t.Errorf("sidecar limit = %v, want %d", idx["limit"], DefaultSimilarLimit)
	}
	works, _ := idx["works"].(map[string]any)
	if len(works) != 2 {
		t.Fatalf("sidecar covers %d works, want 2 (the lonely one has no rail): %v", len(works), works)
	}
	for id := range works {
		if id == "wsimilar00c" {
			t.Error("a Work with no neighbours got an entry; omit it instead of storing an empty rail")
		}
	}
	// The rail names the other Work, with the shared subject that explains it.
	rail, _ := works["wsimilar00a"].([]any)
	if len(rail) != 1 {
		t.Fatalf("rail = %v, want one neighbour", rail)
	}
	n, _ := rail[0].(map[string]any)
	if n["id"] != "wsimilar00b" || n["title"] != "Another Book" {
		t.Errorf("neighbour = %v, want wsimilar00b/Another Book", n)
	}
	shared, _ := n["shared"].([]any)
	if len(shared) != 1 || shared[0] != "https://ex.org/shared" {
		t.Errorf("shared = %v, want the one subject that links them", shared)
	}
}

// --similar=0 means no rail at all. Turning it off must *remove* a sidecar an
// earlier projection wrote, not merely stop rewriting it: the Hugo module renders
// whatever similar.json it finds, so a stale one keeps recommending works that may
// no longer exist. A fresh output directory cannot see this, so project twice.
func TestProjectSimilarZeroRemovesAStaleSidecar(t *testing.T) {
	out := t.TempDir()
	nq := twoRelatedWorks(t)
	if err := projectCatalog(projectOptions{CatalogPath: nq, Providers: []string{"marc"}, Out: out, SimilarLimit: DefaultSimilarLimit}); err != nil {
		t.Fatalf("first projection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "similar.json")); err != nil {
		t.Fatalf("first projection wrote no sidecar: %v", err)
	}
	if err := projectCatalog(projectOptions{CatalogPath: nq, Providers: []string{"marc"}, Out: out, SimilarLimit: 0}); err != nil {
		t.Fatalf("second projection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "similar.json")); !os.IsNotExist(err) {
		t.Fatalf("--similar=0 left the previous similar.json in place (stat err = %v)", err)
	}
}

// A [project] block with no `similar` key gets the default; an explicit 0 disables.
// An int field could not tell those apart, which is why the config carries a pointer.
func TestSimilarLimitDistinguishesUnsetFromZero(t *testing.T) {
	zero := 0
	five := 5
	for _, tc := range []struct {
		name string
		step projectStep
		want int
	}{
		{"unset", projectStep{}, DefaultSimilarLimit},
		{"explicit zero", projectStep{Similar: &zero}, 0},
		{"explicit value", projectStep{Similar: &five}, 5},
	} {
		if got := tc.step.similarLimit(); got != tc.want {
			t.Errorf("%s: similarLimit() = %d, want %d", tc.name, got, tc.want)
		}
	}
}
