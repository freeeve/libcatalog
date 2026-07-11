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
			{ID: "w4", Title: "D"}, // no subjects: dilutes coverage
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
	if r.TotalWorks != 4 || r.CoveredWorks != 3 {
		t.Errorf("totals = %d/%d, want 4/3", r.CoveredWorks, r.TotalWorks)
	}
	got := map[string]int{}
	for _, c := range r.Categories {
		got[c.ID] = c.Works
	}
	for id, want := range map[string]int{"lgbtqia": 1, "immigrant-diaspora": 1, "women-gender": 1} {
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
