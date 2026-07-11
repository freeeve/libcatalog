package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/freeeve/libcat/diversity"
	"github.com/freeeve/libcat/project"
)

// writeCatalog writes a minimal projected catalog.json and returns its path.
func writeCatalog(t *testing.T, cat project.Catalog) string {
	t.Helper()
	data, err := json.Marshal(cat)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// subj is a label-only projected subject (the common ILS shape).
func subj(label string) project.Subject {
	return project.Subject{Labels: map[string]string{"en": label}}
}

// TestRunAuditJSON exercises the whole command over a small catalog and checks the
// coverage-first JSON report.
func TestRunAuditJSON(t *testing.T) {
	cat := project.Catalog{
		Version: 1,
		Works: []project.Work{
			{ID: "w1", Title: "A", Subjects: []project.Subject{subj("Lesbian fiction")}},
			{ID: "w2", Title: "B", Subjects: []project.Subject{subj("Immigrants"), subj("Women authors")}},
			{ID: "w3", Title: "C", Subjects: []project.Subject{subj("Cooking")}},
			{ID: "w4", Title: "D"}, // no signal: dilutes coverage
			{ID: "w5", Title: "E", Tags: []string{"LGBTQIA+ (Fiction)"}}, // tag-only work counts
		},
	}
	catPath := writeCatalog(t, cat)
	outPath := filepath.Join(t.TempDir(), "report.json")

	if err := runAudit([]string{"--catalog", catPath, "--format", "json", "--out", outPath}); err != nil {
		t.Fatalf("runAudit: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	var r diversity.Report
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("parse report: %v", err)
	}
	if r.TotalWorks != 5 || r.CoveredWorks != 4 {
		t.Errorf("totals = %d/%d, want 5 total / 4 covered (the tag-only work counts)", r.TotalWorks, r.CoveredWorks)
	}
	got := map[string]int{}
	for _, c := range r.Categories {
		got[c.ID] = c.Works
	}
	for id, want := range map[string]int{"lgbtqia": 2, "immigrant-diaspora": 1, "women-gender": 1} {
		if got[id] != want {
			t.Errorf("category %s works = %d, want %d", id, got[id], want)
		}
	}
}

// TestRunAuditText checks the text report leads with coverage and lists categories.
func TestRunAuditText(t *testing.T) {
	cat := project.Catalog{Works: []project.Work{
		{ID: "w1", Subjects: []project.Subject{subj("Gay men")}},
		{ID: "w2"},
	}}
	catPath := writeCatalog(t, cat)
	outPath := filepath.Join(t.TempDir(), "report.txt")
	if err := runAudit([]string{"--catalog", catPath, "--out", outPath}); err != nil {
		t.Fatalf("runAudit: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "50.0% coverage") {
		t.Errorf("text report missing coverage line:\n%s", text)
	}
	if !strings.Contains(text, "LGBTQIA+") {
		t.Errorf("text report missing category label:\n%s", text)
	}
}

// TestRunAuditRequiresCatalog checks the required-flag guard.
func TestRunAuditRequiresCatalog(t *testing.T) {
	if err := runAudit([]string{"--format", "json"}); err == nil {
		t.Fatal("audit without --catalog should error")
	}
}

// TestRunAuditFilter is the scoping ask: --filter key=value audits only
// the matching sub-collection (comma-joined extras match per element), --source is
// sugar for the sources extra, and the JSON report names its scope.
func TestRunAuditFilter(t *testing.T) {
	cat := project.Catalog{Works: []project.Work{
		{ID: "w1", Subjects: []project.Subject{subj("Lesbians")},
			Extra: map[string]string{"inQll": "true", "sources": "coll, qll"}},
		{ID: "w2", Subjects: []project.Subject{subj("Gay men")},
			Extra: map[string]string{"sources": "coll"}},
		{ID: "w3"}, // no extras at all: excluded by any filter
	}}
	catPath := writeCatalog(t, cat)

	run := func(args ...string) map[string]any {
		t.Helper()
		outPath := filepath.Join(t.TempDir(), "report.json")
		args = append([]string{"--catalog", catPath, "--format", "json", "--out", outPath}, args...)
		if err := runAudit(args); err != nil {
			t.Fatalf("runAudit(%v): %v", args, err)
		}
		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		var r map[string]any
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}

	if r := run("--filter", "inQll=true"); r["totalWorks"].(float64) != 1 {
		t.Errorf("--filter inQll=true audited %v works, want 1", r["totalWorks"])
	} else if r["scope"] != "inQll=true" {
		t.Errorf("scope = %v, want inQll=true", r["scope"])
	}
	// --source matches an element of the comma-joined sources extra.
	if r := run("--source", "qll"); r["totalWorks"].(float64) != 1 {
		t.Errorf("--source qll audited %v works, want 1 (w1 only)", r["totalWorks"])
	}
	if r := run("--source", "coll"); r["totalWorks"].(float64) != 2 {
		t.Errorf("--source coll audited %v works, want 2", r["totalWorks"])
	}
	// Unfiltered still sees everything and reports no scope.
	if r := run(); r["totalWorks"].(float64) != 3 {
		t.Errorf("unfiltered audited %v works, want 3", r["totalWorks"])
	} else if _, has := r["scope"]; has {
		t.Error("unfiltered report should omit scope")
	}
	// A malformed filter term errors at parse time (the flag set exits the
	// process on error, so assert on the Value directly).
	var ff filterFlags
	if err := ff.Set("novalue"); err == nil {
		t.Error("--filter without key=value should error")
	}
}

// TestRunAuditGraph is the graph mode: --graph audits a catalog.nq
// dataset directly (the full corpus), resolving each subject's URI, its
// skos:prefLabel/rdfs:label, and its scheme from the URI namespace -- so a
// Homosaurus subject with a keyword-less label still counts via the scheme
// dimension, and blank-node bare-string topics count via keywords. --filter
// reads the lcat extra/* work properties.
func TestRunAuditGraph(t *testing.T) {
	const extraNS = "https://github.com/freeeve/libcat/ns#extra/"
	nq := `<#w1Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:coll> .
<#w1Work> <http://id.loc.gov/ontologies/bibframe/subject> <https://homosaurus.org/v5/homoit0000506> <feed:coll> .
<https://homosaurus.org/v5/homoit0000506> <http://www.w3.org/2004/02/skos/core#prefLabel> "Chosen family"@en <feed:coll> .
<#w1Work> <` + extraNS + `inQll> "true" <feed:coll> .
<#w2Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:coll> .
<#w2Work> <http://id.loc.gov/ontologies/bibframe/subject> _:t1 <feed:coll> .
_:t1 <http://www.w3.org/2000/01/rdf-schema#label> "Lesbians" <feed:coll> .
<#w2Work> <` + extraNS + `inQll> "false" <feed:coll> .
<#w3Work> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:coll> .
`
	nqPath := filepath.Join(t.TempDir(), "catalog.nq")
	if err := os.WriteFile(nqPath, []byte(nq), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) map[string]any {
		t.Helper()
		outPath := filepath.Join(t.TempDir(), "report.json")
		args = append([]string{"--graph", nqPath, "--format", "json", "--out", outPath}, args...)
		if err := runAudit(args); err != nil {
			t.Fatalf("runAudit(%v): %v", args, err)
		}
		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatal(err)
		}
		var r map[string]any
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}

	r := run()
	if r["totalWorks"].(float64) != 3 || r["coveredWorks"].(float64) != 2 {
		t.Errorf("graph totals = %v/%v, want 3 total / 2 covered", r["totalWorks"], r["coveredWorks"])
	}
	lgbtqia := 0.0
	for _, c := range r["categories"].([]any) {
		cat := c.(map[string]any)
		if cat["id"] == "lgbtqia" {
			lgbtqia = cat["works"].(float64)
		}
	}
	// w1 counts via the homosaurus SCHEME (its label has no seed keyword);
	// w2 counts via the plural-tolerant KEYWORD on a blank-node topic.
	if lgbtqia != 2 {
		t.Errorf("graph lgbtqia works = %v, want 2 (scheme + keyword paths)", lgbtqia)
	}

	// --filter reads the extra/* work props in graph mode.
	if r := run("--filter", "inQll=true"); r["totalWorks"].(float64) != 1 {
		t.Errorf("graph --filter inQll=true = %v works, want 1", r["totalWorks"])
	}

	// --graph and --catalog are mutually exclusive; neither errors too.
	if err := runAudit([]string{"--graph", nqPath, "--catalog", "x.json"}); err == nil {
		t.Error("--graph with --catalog should error")
	}
	if err := runAudit([]string{"--format", "json"}); err == nil {
		t.Error("neither input should error")
	}
}
