package editor

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/project"

	"github.com/freeeve/libcat/backend/profiles"
)

// applyAndProject runs ops over a real grain, then projects the result --
// the acceptance lens: edits must reach catalog.json.
func applyAndProject(t *testing.T, m *Mapper, grain []byte, workID string, ops []Op) ([]byte, project.Work) {
	t.Helper()
	updated, err := ApplyOps(m, grain, workID, ops, nil)
	if err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}
	cat, err := project.Project(updated, "marc")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range cat.Works {
		if w.ID == workID {
			return updated, w
		}
	}
	t.Fatalf("work %s missing from projection", workID)
	return nil, project.Work{}
}

func firstWork(t *testing.T) (string, []byte) {
	t.Helper()
	for workID, grain := range realGrains(t) {
		m := newMapper(t)
		doc, err := m.ToDoc(grain, workID)
		if err != nil {
			t.Fatal(err)
		}
		if len(doc.Work.Fields["title"]) > 0 {
			return workID, grain
		}
	}
	t.Fatal("no grain with a title")
	return "", nil
}

func TestSetTitleOverridesFeed(t *testing.T) {
	m := newMapper(t)
	workID, grain := firstWork(t)
	updated, w := applyAndProject(t, m, grain, workID, []Op{{
		Resource: "work", Path: "title", Action: "set",
		Values: []OpValue{{V: "The Corrected Title"}},
	}})
	if w.Title != "The Corrected Title" {
		t.Fatalf("projected title = %q", w.Title)
	}
	// Feed statements untouched; marker + skolem structure editorial.
	text := string(updated)
	if !strings.Contains(text, bibframe.PredOverrides) {
		t.Fatal("override marker missing")
	}
	if !strings.Contains(text, "-ed-title") {
		t.Fatal("skolem structure node missing")
	}
	// The doc round-trips: title shows the editorial value, feed flagged.
	doc, err := m.ToDoc(updated, workID)
	if err != nil {
		t.Fatal(err)
	}
	var editorial, overriddenFeed bool
	for _, v := range doc.Work.Fields["title"] {
		if v.Prov == "editorial:" && v.V == "The Corrected Title" {
			editorial = true
		}
		if strings.HasPrefix(v.Prov, "feed:") && v.Overridden {
			overriddenFeed = true
		}
	}
	if !editorial || !overriddenFeed {
		t.Fatalf("doc after set = %+v", doc.Work.Fields["title"])
	}
}

func TestAddRemoveTagLifecycle(t *testing.T) {
	m := newMapper(t)
	workID, grain := firstWork(t)
	// Add an editorial tag (direct field, no override needed).
	updated, w := applyAndProject(t, m, grain, workID, []Op{{
		Resource: "work", Path: "tags", Action: "add",
		Value: &OpValue{V: "cozy fantasy"},
	}})
	if !slices.Contains(w.Tags, "cozy fantasy") {
		t.Fatalf("tags = %v", w.Tags)
	}
	if strings.Contains(string(updated), bibframe.PredOverrides) {
		t.Fatal("plain add should not claim the field")
	}
	// Removing that editorial tag retracts it exactly (grain returns to
	// the original bytes).
	restored, err := ApplyOps(m, updated, workID, []Op{{
		Resource: "work", Path: "tags", Action: "remove",
		Value: &OpValue{V: "cozy fantasy"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(grain) {
		t.Fatal("remove of an editorial add did not restore the grain")
	}
}

func TestRemoveFeedSubjectKeepsSiblings(t *testing.T) {
	m := newMapper(t)
	// Find a work with >=2 feed tag values via the doc.
	for workID, grain := range realGrains(t) {
		doc, err := m.ToDoc(grain, workID)
		if err != nil {
			t.Fatal(err)
		}
		// The MARC fixture carries 650s as feed subject IRIs? Use tags via
		// blank nodes -- the mapper's "tags" field is lcat:tag only, so use
		// the subjects field (IRI-valued) when present, else skip.
		subjects := doc.Work.Fields["subjects"]
		if len(subjects) < 2 || !strings.HasPrefix(subjects[0].Prov, "feed:") {
			continue
		}
		drop := subjects[0]
		keep := subjects[1]
		updated, w := applyAndProject(t, m, grain, workID, []Op{{
			Resource: "work", Path: "subjects", Action: "remove",
			Value: &OpValue{V: drop.V, IRI: true},
		}})
		for _, s := range w.Subjects {
			if s.ID == drop.V {
				t.Fatalf("removed subject still projects: %+v", w.Subjects)
			}
		}
		var kept bool
		for _, s := range w.Subjects {
			kept = kept || s.ID == keep.V
		}
		if !kept {
			t.Fatalf("sibling subject lost: %+v (want %s)", w.Subjects, keep.V)
		}
		// Feed untouched: the dropped subject's feed quad is still in the
		// grain, just shadowed.
		if !strings.Contains(string(updated), drop.V) {
			t.Fatal("feed statement physically removed")
		}
		return
	}
	t.Skip("no fixture work with two feed subject IRIs")
}

func TestOpValidation(t *testing.T) {
	m := newMapper(t)
	workID, grain := firstWork(t)
	cases := map[string]Op{
		"unknown field":    {Resource: "work", Path: "nope", Action: "add", Value: &OpValue{V: "x"}},
		"unknown action":   {Resource: "work", Path: "tags", Action: "upsert", Value: &OpValue{V: "x"}},
		"empty value":      {Resource: "work", Path: "tags", Action: "add", Value: &OpValue{V: ""}},
		"literal for iri":  {Resource: "work", Path: "subjects", Action: "add", Value: &OpValue{V: "not-iri"}},
		"iri for literal":  {Resource: "work", Path: "tags", Action: "add", Value: &OpValue{V: "https://x", IRI: true}},
		"multi for max1":   {Resource: "work", Path: "title", Action: "set", Values: []OpValue{{V: "a"}, {V: "b"}}},
		"unknown instance": {Resource: "izzznope", Path: "isbn", Action: "add", Value: &OpValue{V: "1"}},
		"remove missing":   {Resource: "work", Path: "tags", Action: "remove", Value: &OpValue{V: "never added"}},
		"read-only field":  {Resource: "work", Path: "contributors", Action: "add", Value: &OpValue{V: "Doe, Jane"}},
		"read-only clear":  {Resource: "work", Path: "subjectLabels", Action: "clear"},
	}
	for name, op := range cases {
		if _, err := ApplyOps(m, grain, workID, []Op{op}, nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestAddSubjectWritesLabelCompanion covers an editorial subject
// add also writes the vocabulary's prefLabel into the grain's
// authority:<scheme> graph (feed parity with ingest.enrichmentQuads), so the
// doc annotates -- and the Duplicates compare shows names -- with no vocab
// lookup at read time. An IRI the resolver does not know still adds cleanly,
// just bare.
func TestAddSubjectWritesLabelCompanion(t *testing.T) {
	m := newMapper(t)
	workID, grain := firstWork(t)
	const known = "https://homosaurus.org/v3/homoit0000508"
	const unknown = "https://example.org/authority/unknown"
	resolver := func(iri string) (string, map[string]string, bool) {
		if iri == known {
			// Both languages ride the grain (the projection localizes from
			// them); the doc annotation picks one (English first).
			return "homosaurus", map[string]string{"en": "Gay men", "es": "Hombres gais"}, true
		}
		return "", nil, false
	}
	updated, err := ApplyOps(m, grain, workID, []Op{
		{Resource: "work", Path: "subjects", Action: "add", Value: &OpValue{V: known, IRI: true}},
		{Resource: "work", Path: "subjects", Action: "add", Value: &OpValue{V: unknown, IRI: true}},
	}, resolver)
	if err != nil {
		t.Fatalf("ApplyOps: %v", err)
	}
	companion := "<" + known + "> <http://www.w3.org/2004/02/skos/core#prefLabel> \"Gay men\"@en <authority:homosaurus> ."
	if !strings.Contains(string(updated), companion) {
		t.Fatalf("label companion missing from grain:\n%s", updated)
	}
	if strings.Contains(string(updated), "<"+unknown+"> <http://www.w3.org/2004/02/skos/core#prefLabel>") {
		t.Fatal("unresolvable IRI grew a label")
	}
	// The doc now annotates the value from the grain alone.
	doc, err := m.ToDoc(updated, workID)
	if err != nil {
		t.Fatal(err)
	}
	var annotated bool
	for _, v := range doc.Work.Fields["subjects"] {
		if v.V == known && v.Annotation == "Gay men" {
			annotated = true
		}
		if v.V == unknown && v.Annotation != "" {
			t.Fatalf("unknown term annotated %q", v.Annotation)
		}
	}
	if !annotated {
		t.Fatalf("subject not annotated: %+v", doc.Work.Fields["subjects"])
	}
	// Re-adding the same term is a no-op for the companion too (idempotent
	// patch additions) -- the grain does not grow duplicate labels.
	again, err := ApplyOps(m, updated, workID, []Op{
		{Resource: "work", Path: "subjects", Action: "remove", Value: &OpValue{V: known, IRI: true}},
	}, resolver)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(again), companion); n != 1 {
		t.Fatalf("companion count after remove = %d, want 1 (labels persist)", n)
	}
}

// TestCardinalityBeyondOne covers field.Max is enforced past the
// old Max==1 special case -- an oversized set is rejected, and adds stop once
// the live values reach the cap.
func TestCardinalityBeyondOne(t *testing.T) {
	m := newMapper(t)
	prof := *m.WorkProfile
	prof.Fields = append([]profiles.Field(nil), prof.Fields...)
	workID, grain := firstWork(t)
	doc, err := m.ToDoc(grain, workID)
	if err != nil {
		t.Fatal(err)
	}
	live := 0
	for _, v := range doc.Work.Fields["tags"] {
		if !v.Overridden {
			live++
		}
	}
	max := live + 1
	for i, f := range prof.Fields {
		if f.Path == "tags" {
			prof.Fields[i].Max = max
		}
	}
	m.WorkProfile = &prof

	over := make([]OpValue, max+1)
	for i := range over {
		over[i] = OpValue{V: fmt.Sprintf("tag-%d", i)}
	}
	if _, err := ApplyOps(m, grain, workID, []Op{{Resource: "work", Path: "tags", Action: "set", Values: over}}, nil); err == nil {
		t.Errorf("set of %d into a max-%d field accepted", len(over), max)
	}
	// One add fits (live+1 == max); a second overflows.
	g2, err := ApplyOps(m, grain, workID, []Op{{Resource: "work", Path: "tags", Action: "add", Value: &OpValue{V: "cap-a"}}}, nil)
	if err != nil {
		t.Fatalf("add within cap: %v", err)
	}
	if _, err := ApplyOps(m, g2, workID, []Op{{Resource: "work", Path: "tags", Action: "add", Value: &OpValue{V: "cap-b"}}}, nil); err == nil {
		t.Errorf("add past a max-%d field accepted", max)
	}
}
