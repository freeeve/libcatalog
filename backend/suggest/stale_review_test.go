package suggest

import "testing"

// the reported race: two moderators work the same queue, A resolves
// a suggestion, and B's decision -- staged before A applied -- arrives stale.
// Review is right to refuse it. The bug was that it said nothing, so the caller
// counted the request back to the human as work done.
func TestReviewReportsDecisionsAnotherModeratorResolvedFirst(t *testing.T) {
	svc, _ := newService(t)
	submit(t, svc, "wabc123def456", folk("cozy fantasy"), TypeAdd, "h1")

	decision := func(approve bool, note string) Decision {
		return Decision{
			WorkID: "wabc123def456", Term: folk("cozy fantasy"), Type: TypeAdd,
			Approve: approve, Note: note,
		}
	}

	// A rejects it. A real transition.
	res, err := svc.Review(t.Context(), []Decision{decision(false, "A rejects")}, "a@example.org")
	if err != nil {
		t.Fatalf("A's review: %v", err)
	}
	if res.Applied != 1 || len(res.Skipped) != 0 {
		t.Fatalf("A's review = %+v, want applied 1, skipped 0", res)
	}

	// B approves the same suggestion; their queue page predates A's apply.
	res, err = svc.Review(t.Context(), []Decision{decision(true, "B approves")}, "b@example.org")
	if err != nil {
		t.Fatalf("B's review: %v", err)
	}
	if res.Applied != 0 {
		t.Fatalf("B's stale approve was applied: %+v", res)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Note != "B approves" {
		t.Fatalf("B's stale approve was not reported as skipped: %+v", res)
	}

	// The state is A's, untouched.
	items, err := svc.ForWork(t.Context(), "wabc123def456")
	if err != nil || len(items) != 1 {
		t.Fatalf("ForWork: %+v %v", items, err)
	}
	if items[0].Status != StatusRejected || items[0].ReviewNote != "A rejects" {
		t.Fatalf("B's stale approve changed the record: %+v", items[0])
	}
}

// A decision naming a suggestion that never existed is skipped too, and was
// likewise counted as reviewed.
func TestReviewSkipsDecisionsForSuggestionsThatDoNotExist(t *testing.T) {
	svc, _ := newService(t)
	res, err := svc.Review(t.Context(), []Decision{{
		WorkID: "wabc123def456", Term: folk("never suggested"), Type: TypeAdd, Approve: true,
	}}, "mod@example.org")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if res.Applied != 0 || len(res.Skipped) != 1 {
		t.Fatalf("review of a nonexistent suggestion = %+v, want applied 0, skipped 1", res)
	}
}

// A mixed batch reports each half. The reported case: 2 decisions submitted,
// 1 applied, and the response said "reviewed 2".
func TestReviewCountsOnlyTheDecisionsItApplied(t *testing.T) {
	svc, _ := newService(t)
	submit(t, svc, "wabc123def456", folk("stale one"), TypeAdd, "h1")
	submit(t, svc, "wzzz999zzz999", folk("live one"), TypeAdd, "h2")

	stale := Decision{WorkID: "wabc123def456", Term: folk("stale one"), Type: TypeAdd, Approve: false, Note: "first"}
	if res, err := svc.Review(t.Context(), []Decision{stale}, "a@example.org"); err != nil || res.Applied != 1 {
		t.Fatalf("seed: %+v %v", res, err)
	}

	live := Decision{WorkID: "wzzz999zzz999", Term: folk("live one"), Type: TypeAdd, Approve: true, Note: "second"}
	stale.Note = "stale retry"
	res, err := svc.Review(t.Context(), []Decision{stale, live}, "b@example.org")
	if err != nil {
		t.Fatalf("mixed batch: %v", err)
	}
	if res.Applied != 1 {
		t.Fatalf("applied = %d, want 1 (the live decision only)", res.Applied)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Note != "stale retry" {
		t.Fatalf("skipped = %+v, want the stale decision", res.Skipped)
	}
}

// The audit trail was already honest -- a skipped decision writes no entry.
// Lock that in: the count and the trail must agree.
func TestSkippedDecisionsWriteNoAuditEntry(t *testing.T) {
	svc, _ := newService(t)
	submit(t, svc, "wabc123def456", folk("cozy fantasy"), TypeAdd, "h1")
	d := Decision{WorkID: "wabc123def456", Term: folk("cozy fantasy"), Type: TypeAdd, Approve: false}

	if _, err := svc.Review(t.Context(), []Decision{d}, "a@example.org"); err != nil {
		t.Fatal(err)
	}
	d.Approve = true
	res, err := svc.Review(t.Context(), []Decision{d}, "b@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("expected B's decision to be skipped: %+v", res)
	}

	entries, err := svc.Audit(t.Context(), svc.now().UTC().Format("2006-01"))
	if err != nil {
		t.Fatal(err)
	}
	var rejects, approves int
	for _, e := range entries {
		switch e.Action {
		case "REVIEW_REJECT":
			rejects++
		case "REVIEW_APPROVE":
			approves++
		}
	}
	if rejects != 1 || approves != 0 {
		t.Fatalf("audit = %d reject / %d approve, want 1 / 0: the skipped approve left a trace", rejects, approves)
	}
}
