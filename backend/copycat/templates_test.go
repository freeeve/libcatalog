package copycat_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/freeeve/libcat/backend/copycat"
	"github.com/freeeve/libcat/backend/marcview"
)

// TestTemplates pins the shipped skeletons: well-formed fixed fields, a 245
// present, and -- once titled -- passing the original-record gate.
func TestTemplates(t *testing.T) {
	templates, err := copycat.LoadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) != 4 {
		t.Fatalf("templates = %d, want 4", len(templates))
	}
	for _, tpl := range templates {
		if len(tpl.Record.Leader) != 24 {
			t.Errorf("%s: leader length = %d", tpl.ID, len(tpl.Record.Leader))
		}
		has245 := false
		for _, f := range tpl.Record.Fields {
			if f.Tag == "008" && len(f.Value) != 40 {
				t.Errorf("%s: 008 length = %d", tpl.ID, len(f.Value))
			}
			if f.Tag == "006" && len(f.Value) != 18 {
				t.Errorf("%s: 006 length = %d", tpl.ID, len(f.Value))
			}
			if f.Tag == "245" {
				has245 = true
			}
		}
		if !has245 {
			t.Errorf("%s: no 245 in the skeleton", tpl.ID)
		}
		// A skeleton with just the title filled must pass the gate.
		doc := tpl.Record
		fields := make([]marcview.Field, len(doc.Fields))
		copy(fields, doc.Fields)
		for i, f := range fields {
			if f.Tag == "245" {
				subs := make([]marcview.Subfield, len(f.Subfields))
				copy(subs, f.Subfields)
				subs[0].Value = "A title"
				fields[i].Subfields = subs
			}
		}
		doc.Fields = fields
		if errs := copycat.ValidateOriginal(doc); len(errs) != 0 {
			t.Errorf("%s: titled skeleton fails the gate: %+v", tpl.ID, errs)
		}
	}
}

// TestStageOriginal is the tasks/077 acceptance: a titled draft stages as an
// "original" batch with empty skeleton rows pruned; an untitled or malformed
// draft is refused with field-anchored errors.
func TestStageOriginal(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := t.Context()
	templates, err := copycat.LoadTemplates()
	if err != nil {
		t.Fatal(err)
	}
	var book copycat.Template
	for _, tpl := range templates {
		if tpl.ID == "book" {
			book = tpl
		}
	}

	// Refused untitled: the 245 error is field-anchored.
	_, _, fieldErrs, err := svc.StageOriginal(ctx, "", book.Record, "lib@example.org")
	if !errors.Is(err, copycat.ErrValidation) || len(fieldErrs) == 0 || fieldErrs[0].Tag != "245" {
		t.Fatalf("untitled: errs=%+v err=%v", fieldErrs, err)
	}

	// Title it and break the leader: LDR-anchored refusal.
	doc := book.Record
	doc.Fields[3].Subfields = []marcview.Subfield{{Code: "a", Value: "Original works"}}
	doc.Leader = "short"
	_, _, fieldErrs, err = svc.StageOriginal(ctx, "", doc, "lib@example.org")
	if !errors.Is(err, copycat.ErrValidation) || len(fieldErrs) != 1 || fieldErrs[0].Tag != "LDR" {
		t.Fatalf("bad leader: errs=%+v err=%v", fieldErrs, err)
	}

	// Valid: stages with source "original", empty skeleton rows pruned.
	doc.Leader = book.Record.Leader
	batch, records, fieldErrs, err := svc.StageOriginal(ctx, "", doc, "lib@example.org")
	if err != nil || len(fieldErrs) != 0 {
		t.Fatalf("stage: errs=%+v err=%v", fieldErrs, err)
	}
	if batch.Source != "original" || !strings.Contains(batch.Label, "Original works") {
		t.Fatalf("batch = %+v", batch)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	for _, f := range records[0].Record.Fields {
		if f.Tag == "020" || f.Tag == "100" {
			t.Errorf("empty skeleton row %s survived staging", f.Tag)
		}
	}
	if records[0].Match.MatchedWork {
		t.Fatalf("fresh corpus should not match: %+v", records[0].Match)
	}

	// The staged batch commits through the normal pipeline.
	done, err := svc.Commit(ctx, batch.ID, "lib@example.org")
	if err != nil || done.Committed != 1 {
		t.Fatalf("commit = %+v, %v", done, err)
	}
}
