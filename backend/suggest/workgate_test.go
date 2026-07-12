package suggest

import (
	"context"
	"errors"
	"testing"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/vocab"
)

// gateService is newService plus a WorkState stub over a fixed catalog.
func gateService(t *testing.T, works map[string]bool) (*Service, *store.Mem) {
	t.Helper()
	svc, db := newService(t)
	svc.WorkState = func(_ context.Context, workID string) (bool, bool, error) {
		dead, ok := works[workID]
		return ok, dead, nil
	}
	return svc, db
}

// TestSubmitRefusesGhostAndTombstonedWorks pins the intake work gate (task
// 441): a well-formed id the catalog never had, and a tombstoned work, both
// answer like a tombstoned pair -- refused, never queued, and
// indistinguishable from the pair tombstone so the anonymous endpoint is no
// existence oracle. A live work still queues.
func TestSubmitRefusesGhostAndTombstonedWorks(t *testing.T) {
	svc, _ := gateService(t, map[string]bool{
		"wlive0000000001": false,
		"wdead0000000001": true,
	})
	in := func(workID string) SubmitInput {
		return SubmitInput{WorkID: workID, Term: controlled(transURI), Type: TypeAdd, SupporterHash: "h1", WorkTitle: "X"}
	}
	if _, err := svc.Submit(t.Context(), in("wzzghost0000e2e")); !errors.Is(err, ErrTombstoned) {
		t.Fatalf("ghost work: err = %v, want ErrTombstoned", err)
	}
	if _, err := svc.Submit(t.Context(), in("wdead0000000001")); !errors.Is(err, ErrTombstoned) {
		t.Fatalf("tombstoned work: err = %v, want ErrTombstoned", err)
	}
	if _, err := svc.Submit(t.Context(), in("wlive0000000001")); err != nil {
		t.Fatalf("live work refused: %v", err)
	}
	page, err := svc.Queue(t.Context(), QueueQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sg := range page.Items {
		if sg.WorkID != "wlive0000000001" {
			t.Fatalf("a refused work reached the queue: %+v", sg)
		}
	}
}

// TestTombstoningRejectsOpenRows pins the cleanup half of task 441:
// RejectOpenForWork closes a work's PENDING rows with a moderator-grade
// reject and leaves already-resolved rows alone.
func TestTombstoningRejectsOpenRows(t *testing.T) {
	svc, _ := newService(t)
	submit(t, svc, "wretiree000001", controlled(transURI), TypeAdd, "h1")
	submit(t, svc, "wretiree000001", vocab.TermRef{Scheme: "lcsh", ID: sciFiURI}, TypeAdd, "h2")
	// Resolve one of the two first: it must stay APPROVED.
	if _, err := svc.Review(t.Context(), []Decision{
		{WorkID: "wretiree000001", Term: controlled(transURI), Type: TypeAdd, Approve: true},
	}, "mod@example.org"); err != nil {
		t.Fatal(err)
	}

	closed, err := svc.RejectOpenForWork(t.Context(), "wretiree000001", "lib@example.org", "work tombstoned")
	if err != nil || closed != 1 {
		t.Fatalf("RejectOpenForWork = %d, %v; want 1 closed", closed, err)
	}
	rows, err := svc.ForWork(t.Context(), "wretiree000001")
	if err != nil {
		t.Fatal(err)
	}
	byStatus := map[Status]int{}
	for _, sg := range rows {
		byStatus[sg.Status]++
	}
	if byStatus[StatusRejected] != 1 || byStatus[StatusApproved] != 1 {
		t.Fatalf("rows after cleanup = %v, want 1 REJECTED + 1 APPROVED kept", byStatus)
	}
}

// TestRejectClearsApprovedUnpublishedGhost pins the task 442 exit: an
// APPROVED row that never published (its work's grain is gone, the publisher
// skips it forever) accepts a moderator reject; a row that DID publish stays
// resolved -- undoing it means editing the graph, not the queue.
func TestRejectClearsApprovedUnpublishedGhost(t *testing.T) {
	svc, _ := newService(t)
	submit(t, svc, "wghost00000001", controlled(transURI), TypeAdd, "h1")
	submit(t, svc, "wshipped000001", vocab.TermRef{Scheme: "lcsh", ID: sciFiURI}, TypeAdd, "h2")
	if _, err := svc.Review(t.Context(), []Decision{
		{WorkID: "wghost00000001", Term: controlled(transURI), Type: TypeAdd, Approve: true},
		{WorkID: "wshipped000001", Term: vocab.TermRef{Scheme: "lcsh", ID: sciFiURI}, Type: TypeAdd, Approve: true},
	}, "mod@example.org"); err != nil {
		t.Fatal(err)
	}
	// The second row published; the first is the stuck ghost.
	shipped, err := svc.ApprovedUnpublished(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var pub []Suggestion
	for _, sg := range shipped {
		if sg.WorkID == "wshipped000001" {
			pub = append(pub, sg)
		}
	}
	if err := svc.MarkPublished(t.Context(), pub, "etag-1"); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Review(t.Context(), []Decision{
		{WorkID: "wghost00000001", Term: controlled(transURI), Type: TypeAdd, Approve: false, Note: "work is gone"},
		{WorkID: "wshipped000001", Term: vocab.TermRef{Scheme: "lcsh", ID: sciFiURI}, Type: TypeAdd, Approve: false},
	}, "mod@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 1 || len(res.Skipped) != 1 {
		t.Fatalf("review = %+v, want the ghost rejected and the published row skipped", res)
	}
	rows, err := svc.ForWork(t.Context(), "wghost00000001")
	if err != nil || len(rows) != 1 || rows[0].Status != StatusRejected {
		t.Fatalf("ghost after reject = %+v, %v; want REJECTED", rows, err)
	}
	kept, err := svc.ForWork(t.Context(), "wshipped000001")
	if err != nil || len(kept) != 1 || kept[0].Status != StatusApproved {
		t.Fatalf("published row after reject attempt = %+v, %v; want still APPROVED", kept, err)
	}
}
