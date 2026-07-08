package bibframe

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/storage"
	codexbf "github.com/freeeve/libcodex/bibframe"
)

func TestScanPins(t *testing.T) {
	grain := []byte("<#isame2Instance> <" + PredWorkAssignment + "> <#wnewWork> <editorial:> .\n" +
		"<#wnewWork> <" + PredSplitFrom + "> <#wsharedWork> <editorial:> .\n")
	got, err := ScanPins(grain)
	if err != nil {
		t.Fatalf("ScanPins: %v", err)
	}
	want := []identity.Pin{{Instance: "isame2", Work: "wnew"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ScanPins = %v, want %v", got, want)
	}
}

func TestAddSplitMarkersIdempotent(t *testing.T) {
	base := []byte("<#wsharedWork> <http://id.loc.gov/ontologies/bibframe/mainTitle> \"x\" <feed:overdrive> .\n")
	once, err := AddSplitMarkers(base, "wnew", "wshared", []string{"isame2"})
	if err != nil {
		t.Fatalf("AddSplitMarkers: %v", err)
	}
	for _, want := range []string{PredSplitFrom, PredWorkAssignment} {
		if !bytes.Contains(once, []byte(want)) {
			t.Errorf("split markers missing %q", want)
		}
	}
	twice, err := AddSplitMarkers(once, "wnew", "wshared", []string{"isame2"})
	if err != nil {
		t.Fatalf("AddSplitMarkers (again): %v", err)
	}
	if !bytes.Equal(once, twice) {
		t.Error("AddSplitMarkers is not idempotent")
	}
}

// TestSplitReingest is the over-merge half of the tasks/001 gate: a recorded split
// pins one Instance of an over-merged Work onto a new Work, and the split
// reproduces (and stays byte-stable) across re-ingest.
func TestSplitReingest(t *testing.T) {
	dir := t.TempDir()
	sink := storage.Dir(dir)

	shared := WorkGroup{
		WorkID: "wshared",
		Work:   codexbf.Work{Class: "Text", Titles: []codexbf.Title{{MainTitle: "Ambiguous Title"}}},
		Instances: []GroupInstance{
			{InstanceID: "isame1", Instance: codexbf.Instance{Identifiers: []codexbf.Identifier{{Class: "Isbn", Value: "9780000000111"}}}},
			{InstanceID: "isame2", Instance: codexbf.Instance{Identifiers: []codexbf.Identifier{{Class: "Isbn", Value: "9780000000222"}}}},
		},
	}
	if _, err := BuildWorks(sink, []WorkGroup{shared}, "overdrive"); err != nil {
		t.Fatalf("initial build: %v", err)
	}

	// Record the split of isame2 into a new work (what `lcat split` does).
	sharedPath := filepath.Join(dir, filepath.FromSlash(GrainPath("wshared")))
	marked, err := AddSplitMarkers(readFile(t, sharedPath), "wnew", "wshared", []string{"isame2"})
	if err != nil {
		t.Fatalf("AddSplitMarkers: %v", err)
	}
	if err := os.WriteFile(sharedPath, marked, 0o644); err != nil {
		t.Fatal(err)
	}

	reingestSplit(t, dir, sink)
	sharedGrain := readFile(t, sharedPath)
	newPath := filepath.Join(dir, filepath.FromSlash(GrainPath("wnew")))
	newGrain := readFile(t, newPath)

	if bytes.Contains(sharedGrain, []byte("9780000000222")) {
		t.Error("split Instance still in the source Work")
	}
	if !bytes.Contains(sharedGrain, []byte("9780000000111")) {
		t.Error("source Work lost its own Instance")
	}
	if !bytes.Contains(newGrain, []byte("9780000000222")) {
		t.Error("split Instance not in the new Work")
	}
	if !bytes.Contains(sharedGrain, []byte(PredWorkAssignment)) {
		t.Error("split pin not preserved across re-ingest")
	}

	before := readFile(t, newPath)
	reingestSplit(t, dir, sink)
	if after := readFile(t, newPath); !bytes.Equal(before, after) {
		t.Error("split re-ingest is not byte-stable")
	}
}

// reingestSplit replays one ingest over the grains under dir with a fixed two-
// record fixture the computed key would cluster together, honoring the recovered
// split pins, and rebuilds every resulting Work group.
func reingestSplit(t *testing.T, dir string, sink storage.Sink) {
	t.Helper()
	prior, err := LoadPrior(dir, "overdrive")
	if err != nil {
		t.Fatalf("load prior: %v", err)
	}
	r := identity.NewResolver()
	identity.SeedResolver(r, prior.Grains)
	for _, p := range prior.Pins {
		r.SeedPin(p.Instance, p.Work)
	}

	work := codexbf.Work{Class: "Text", Titles: []codexbf.Title{{MainTitle: "Ambiguous Title"}}}
	recs := []identity.Record{
		{ProviderKeys: []string{identity.ProviderKey(identity.SchemeISBN, "9780000000111")}, Title: "Ambiguous Title"},
		{ProviderKeys: []string{identity.ProviderKey(identity.SchemeISBN, "9780000000222")}, Title: "Ambiguous Title"},
	}
	insts := []codexbf.Instance{
		{Identifiers: []codexbf.Identifier{{Class: "Isbn", Value: "9780000000111"}}},
		{Identifiers: []codexbf.Identifier{{Class: "Isbn", Value: "9780000000222"}}},
	}
	groups := map[string]*WorkGroup{}
	var order []string
	for i, rec := range recs {
		a := r.Resolve(rec)
		g, ok := groups[a.WorkID]
		if !ok {
			g = &WorkGroup{WorkID: a.WorkID, Work: work, Editorial: prior.Editorial[a.WorkID]}
			groups[a.WorkID] = g
			order = append(order, a.WorkID)
		}
		g.Instances = append(g.Instances, GroupInstance{InstanceID: a.InstanceID, Instance: insts[i]})
	}
	built := make([]WorkGroup, 0, len(order))
	for _, id := range order {
		built = append(built, *groups[id])
	}
	if _, err := BuildWorks(sink, built, "overdrive"); err != nil {
		t.Fatalf("re-build: %v", err)
	}
}
