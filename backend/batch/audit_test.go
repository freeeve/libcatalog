package batch_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/freeeve/libcat/bibframe"

	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/editor"
	"github.com/freeeve/libcat/backend/suggest"
)

func auditEntries(t *testing.T, queue *suggest.Service) []suggest.AuditEntry {
	t.Helper()
	entries, err := queue.Audit(t.Context(), time.Now().UTC().Format("2006-01"))
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// a bulk op rewrote a record and the record's own History tab read
// "0 entries", because the run wrote one audit entry carrying no work id. The
// per-work read filters on WorkID, so an entry without one is attributable to
// nothing. Every other write path in the codebase sets it.
func TestRunAuditsEveryChangedRecord(t *testing.T) {
	svc, _, queue, _ := newService(t)
	ctx := t.Context()
	sel := batch.Selection{Kind: batch.KindIDs, IDs: []string{"wbatch0000001", "wbatch0000002"}}
	ops := summarySetOps("A necromantic space opera.")

	// A dry run audits nothing at all.
	if _, err := svc.Run(ctx, sel, ops, true, "lib@example.org"); err != nil {
		t.Fatal(err)
	}
	if got := auditEntries(t, queue); len(got) != 0 {
		t.Fatalf("dry run wrote %d audit entries", len(got))
	}

	if _, err := svc.Run(ctx, sel, ops, false, "lib@example.org"); err != nil {
		t.Fatal(err)
	}
	entries := auditEntries(t, queue)

	perWork := map[string]suggest.AuditEntry{}
	var aggregate []suggest.AuditEntry
	for _, e := range entries {
		if e.Action != "BATCH_OPS" {
			t.Fatalf("unexpected action %q", e.Action)
		}
		if e.WorkID == "" {
			aggregate = append(aggregate, e)
			continue
		}
		perWork[e.WorkID] = e
	}
	if len(aggregate) != 1 {
		t.Fatalf("want exactly one aggregate entry, got %d", len(aggregate))
	}
	if len(perWork) != 2 {
		t.Fatalf("want one entry per changed record, got %d: %+v", len(perWork), perWork)
	}
	for _, id := range sel.IDs {
		e, ok := perWork[id]
		if !ok {
			t.Fatalf("%s has no audit entry", id)
		}
		if e.Actor != "lib@example.org" || e.ETag == "" || e.Note == "" {
			t.Fatalf("%s entry = %+v", id, e)
		}
		// The run id ties this row back to the aggregate row.
		if e.RunID == "" || e.RunID != aggregate[0].RunID {
			t.Fatalf("%s runId = %q, aggregate = %q", id, e.RunID, aggregate[0].RunID)
		}
	}
	// The aggregate note is parseable JSON that names the records it rewrote:
	// unlike a per-record row, it is read by the Audit screen, not a cataloger.
	var note suggest.RunNote
	if err := json.Unmarshal([]byte(aggregate[0].Note), &note); err != nil {
		t.Fatalf("aggregate note is not parseable: %v (%q)", err, aggregate[0].Note)
	}
	if note.Rewritten != 2 || len(note.Works) != 2 {
		t.Fatalf("aggregate note = %+v, want it to name both rewritten works", note)
	}
	for _, id := range sel.IDs {
		if !slices.Contains(note.Works, id) {
			t.Fatalf("the aggregate note does not name %s: %+v", id, note.Works)
		}
	}

	// Two runs are two run ids, or the rows of one cannot be told from another's.
	if _, err := svc.Run(ctx, sel, summarySetOps("Again."), false, "lib@example.org"); err != nil {
		t.Fatal(err)
	}
	runs := map[string]struct{}{}
	for _, e := range auditEntries(t, queue) {
		runs[e.RunID] = struct{}{}
	}
	if len(runs) != 2 {
		t.Fatalf("want 2 distinct run ids across 2 runs, got %d", len(runs))
	}
}

// A record the run could not rewrite has nothing to record. Auditing a failure
// as an edit would be worse than the silence it replaced.
func TestRunAuditsOnlySuccessfulRecords(t *testing.T) {
	svc, _, queue, _ := newService(t)
	sel := batch.Selection{Kind: batch.KindIDs, IDs: []string{"wbatch0000001", "wmissing00001"}}
	run, err := svc.Run(t.Context(), sel, summarySetOps("Summary."), false, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if run.Applied != 1 || run.Failed != 1 {
		t.Fatalf("run = %+v", run)
	}
	for _, e := range auditEntries(t, queue) {
		if e.WorkID == "wmissing00001" {
			t.Fatalf("the failed record was audited as edited: %+v", e)
		}
	}
	var audited int
	for _, e := range auditEntries(t, queue) {
		if e.WorkID != "" {
			audited++
		}
	}
	if audited != 1 {
		t.Fatalf("want 1 per-record entry, got %d", audited)
	}
}

// An item op edits holdings, not work fields, and is audited the same way: the
// record was rewritten, so its history says so.
func TestRunAuditsItemOps(t *testing.T) {
	svc, st, queue, _ := newService(t)
	seedShelvedWork(t, st, "wshelf000009", "ishelf000009", "Gideon the Ninth",
		[]bibframe.Item{{Barcode: "31234", Location: "Stacks"}})
	ops := []editor.Op{{Resource: editor.ResourceItems, Path: "location", Action: "clear"}}
	if _, err := svc.Run(t.Context(), batch.Selection{Kind: batch.KindIDs, IDs: []string{"wshelf000009"}}, ops, false, "lib@example.org"); err != nil {
		t.Fatal(err)
	}
	for _, e := range auditEntries(t, queue) {
		if e.WorkID == "wshelf000009" {
			return
		}
	}
	t.Fatal("an item-op run left no per-record audit entry")
}

// A record the run selected but did not change gets no history entry: its
// history would otherwise claim an edit that never happened. The run itself is
// still recorded, by the aggregate entry.
func TestRunDoesNotAuditUnchangedRecords(t *testing.T) {
	svc, st, queue, _ := newService(t)
	// This work has no holdings, so an item op rewrites nothing.
	seedShelvedWork(t, st, "wshelf000010", "ishelf000010", "Harrow the Ninth", nil)
	ops := []editor.Op{{Resource: editor.ResourceItems, Path: "location", Action: "clear"}}
	run, err := svc.Run(t.Context(), batch.Selection{Kind: batch.KindIDs, IDs: []string{"wshelf000010"}}, ops, false, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if run.Applied != 1 || run.Added != 0 || run.Removed != 0 {
		t.Fatalf("run = %+v, want applied with an empty diff", run)
	}
	entries := auditEntries(t, queue)
	for _, e := range entries {
		if e.WorkID == "wshelf000010" {
			t.Fatalf("an untouched record was audited as edited: %+v", e)
		}
	}
	// The run is still on the record, aggregate-wise.
	if len(entries) != 1 || entries[0].WorkID != "" {
		t.Fatalf("want exactly the aggregate entry, got %+v", entries)
	}
	var note suggest.RunNote
	if err := json.Unmarshal([]byte(entries[0].Note), &note); err != nil {
		t.Fatalf("aggregate note is not parseable: %v (%q)", err, entries[0].Note)
	}
	if note.Applied != 1 || note.Rewritten != 0 || len(note.Works) != 0 {
		t.Fatalf("the aggregate note hides that nothing was rewritten: %+v", note)
	}
}

// The per-record audit must not stop at maxItemDiffs, the point where a run's
// per-record diffs are dropped from the response.
func TestRunAuditsPastTheDiffTruncationCap(t *testing.T) {
	svc, st, queue, _ := newService(t)
	var ids []string
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("wcap%09d", i)
		seedWork(t, st, id, fmt.Sprintf("Book %d", i), "")
		ids = append(ids, id)
	}
	run, err := svc.Run(t.Context(), batch.Selection{Kind: batch.KindIDs, IDs: ids}, summarySetOps("Summary."), false, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if !run.DiffsTruncated {
		t.Fatal("expected the response diffs to be truncated at 60 works")
	}
	audited := map[string]bool{}
	for _, e := range auditEntries(t, queue) {
		if e.WorkID != "" {
			audited[e.WorkID] = true
		}
	}
	if len(audited) != 60 {
		t.Fatalf("%d/60 records audited: truncating the response diffs must not truncate the audit", len(audited))
	}
}
