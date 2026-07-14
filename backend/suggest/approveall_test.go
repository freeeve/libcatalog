package suggest

import (
	"strings"
	"testing"
	"time"

	"github.com/freeeve/libcat/backend/vocab"
)

// thisMonth is the audit partition the just-written entries land in.
func thisMonth() string { return time.Now().UTC().Format("2006-01") }

// pendingCount is the pending-queue size for a scope.
func pendingCount(t *testing.T, svc *Service, q QueueQuery) int {
	t.Helper()
	n, err := svc.CountPending(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// TestApproveAllScopedReversibleAndSingleAudit is the core of the bulk
// approve-all (task 473): it approves only the rows the filter names, leaves
// the rest PENDING, is idempotent on re-run, keeps approved rows rejectable
// (nothing published), and writes ONE aggregate audit entry, not one per row.
func TestApproveAllScopedReversibleAndSingleAudit(t *testing.T) {
	svc, _ := newService(t)
	// Two homosaurus pipeline rows (in scope) and one folk patron row (out).
	if err := svc.PipelineSuggest(t.Context(), "wapprove0001a", controlled(transURI), 0.9); err != nil {
		t.Fatal(err)
	}
	if err := svc.PipelineSuggest(t.Context(), "wapprove0001b", controlled(sciFiURI), 0.9); err != nil {
		t.Fatal(err)
	}
	submit(t, svc, "wapprove0001c", folk("cozy fantasy"), TypeAdd, "h1")

	// Approve only the homosaurus scheme.
	res, err := svc.ApproveAll(t.Context(), QueueQuery{Scheme: "homosaurus"}, "lib@example.org", nil)
	if err != nil {
		t.Fatalf("ApproveAll: %v", err)
	}
	if res.Total != 2 || res.Approved != 2 || res.Skipped != 0 {
		t.Fatalf("result = %+v, want 2 total / 2 approved / 0 skipped", res)
	}
	// The two in-scope rows are APPROVED; the folk row is untouched.
	for _, id := range []string{"wapprove0001a", "wapprove0001b"} {
		items, _ := svc.ForWork(t.Context(), id)
		if len(items) != 1 || items[0].Status != StatusApproved || items[0].ReviewedBy != "lib@example.org" {
			t.Fatalf("%s = %+v, want APPROVED by the actor", id, items)
		}
	}
	if got := pendingCount(t, svc, QueueQuery{Scheme: vocab.FolkScheme}); got != 1 {
		t.Fatalf("folk pending = %d, want 1 (out of scope, untouched)", got)
	}

	// Idempotent: a re-run finds nothing pending in scope.
	res2, err := svc.ApproveAll(t.Context(), QueueQuery{Scheme: "homosaurus"}, "lib@example.org", nil)
	if err != nil || res2.Total != 0 || res2.Approved != 0 {
		t.Fatalf("re-run = %+v, %v, want a no-op", res2, err)
	}

	// Reversible: an approved-but-unpublished row is still rejectable.
	rev, err := svc.Review(t.Context(), []Decision{
		{WorkID: "wapprove0001a", Term: controlled(transURI), Type: TypeAdd, Approve: false},
	}, "lib@example.org")
	if err != nil || rev.Applied != 1 {
		t.Fatalf("un-approve = %+v, %v, want the approved row rejectable", rev, err)
	}
	if items, _ := svc.ForWork(t.Context(), "wapprove0001a"); items[0].Status != StatusRejected {
		t.Fatalf("after un-approve = %+v, want REJECTED", items[0])
	}

	// Audit: exactly one aggregate entry for the bulk run, and NOT one
	// REVIEW_APPROVE per row.
	entries, err := svc.Audit(t.Context(), thisMonth())
	if err != nil {
		t.Fatal(err)
	}
	agg, perRow := 0, 0
	for _, e := range entries {
		switch e.Action {
		case "QUEUE_APPROVE_ALL":
			agg++
			if !strings.Contains(e.Note, "\"applied\":2") || e.RunID == "" {
				t.Errorf("aggregate note = %q (runID %q), want applied:2 and a run id", e.Note, e.RunID)
			}
		case "REVIEW_APPROVE":
			perRow++
		}
	}
	if agg != 1 {
		t.Errorf("aggregate entries = %d, want exactly 1", agg)
	}
	if perRow != 0 {
		t.Errorf("per-row REVIEW_APPROVE entries = %d, want 0 (bulk writes one aggregate)", perRow)
	}
}

// TestApproveAllHonoursConfidenceFloor: the bulk run scopes by the same
// confidence floor the review screen uses, so "everything above X" approves
// only the rows at or above X.
func TestApproveAllHonoursConfidenceFloor(t *testing.T) {
	svc, _ := newService(t)
	if err := svc.PipelineSuggest(t.Context(), "wapprove0002a", controlled(transURI), 0.9); err != nil {
		t.Fatal(err)
	}
	if err := svc.PipelineSuggest(t.Context(), "wapprove0002b", controlled(sciFiURI), 0.4); err != nil {
		t.Fatal(err)
	}
	if got := pendingCount(t, svc, QueueQuery{MinConfidence: 0.6}); got != 1 {
		t.Fatalf("count above 0.6 = %d, want 1", got)
	}
	res, err := svc.ApproveAll(t.Context(), QueueQuery{MinConfidence: 0.6}, "lib@example.org", nil)
	if err != nil || res.Approved != 1 {
		t.Fatalf("approve above 0.6 = %+v, %v, want 1 approved", res, err)
	}
	// The low-confidence row stays PENDING.
	if items, _ := svc.ForWork(t.Context(), "wapprove0002b"); items[0].Status != StatusPending {
		t.Fatalf("below-floor row = %+v, want still PENDING", items[0])
	}
}
