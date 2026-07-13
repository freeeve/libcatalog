package suggest

import (
	"errors"
	"testing"

	"github.com/freeeve/libcat/backend/vocab"
)

func TestQueueAndReview(t *testing.T) {
	svc, _ := newService(t)
	submit(t, svc, "wabc123def456", controlled(transURI), TypeAdd, "h1")
	submit(t, svc, "wabc123def456", controlled(transURI), TypeAdd, "h2")
	submit(t, svc, "wzzz999zzz999", folk("cozy fantasy"), TypeAdd, "h3")

	page, err := svc.Queue(t.Context(), QueueQuery{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("queue = %+v, %v", page, err)
	}
	// Supporter-count order: the 2-vote item first.
	if page.Items[0].SupporterCount != 2 {
		t.Fatalf("order: %+v", page.Items)
	}
	// Scheme filter.
	page, _ = svc.Queue(t.Context(), QueueQuery{Scheme: vocab.FolkScheme})
	if len(page.Items) != 1 || page.Items[0].Term.ID != "cozy fantasy" {
		t.Fatalf("folk filter = %+v", page.Items)
	}

	// Approve one, reject+tombstone the other.
	decisions := []Decision{
		{WorkID: "wabc123def456", Term: controlled(transURI), Type: TypeAdd, Approve: true, Note: "fits"},
		{WorkID: "wzzz999zzz999", Term: folk("cozy fantasy"), Type: TypeAdd, Approve: false, Tombstone: true},
	}
	if _, err := svc.Review(t.Context(), decisions, "lib@example.org"); err != nil {
		t.Fatalf("Review: %v", err)
	}
	// Queue drained.
	page, _ = svc.Queue(t.Context(), QueueQuery{})
	if len(page.Items) != 0 {
		t.Fatalf("pending after review = %+v", page.Items)
	}
	// Approved item stamped.
	items, _ := svc.ForWork(t.Context(), "wabc123def456")
	if items[0].Status != StatusApproved || items[0].ReviewedBy != "lib@example.org" || items[0].ReviewNote != "fits" {
		t.Fatalf("approved = %+v", items[0])
	}
	// Tombstone blocks re-suggestion.
	_, err = svc.Submit(t.Context(), SubmitInput{
		WorkID: "wzzz999zzz999", Term: folk("cozy fantasy"), Type: TypeAdd, SupporterHash: "h9",
	})
	if !errors.Is(err, ErrTombstoned) {
		t.Fatalf("tombstoned resubmit: %v", err)
	}
	// Re-reviewing a resolved item is a no-op, not an error.
	if _, err := svc.Review(t.Context(), decisions[:1], "other@example.org"); err != nil {
		t.Fatalf("re-review: %v", err)
	}
	items, _ = svc.ForWork(t.Context(), "wabc123def456")
	if items[0].ReviewedBy != "lib@example.org" {
		t.Fatal("resolved item re-stamped")
	}
	// Audit trail captured everything, newest first.
	month := svc.now().UTC().Format("2006-01")
	entries, err := svc.Audit(t.Context(), month)
	if err != nil || len(entries) != 2 {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
}

func TestReviewSubstitute(t *testing.T) {
	svc, _ := newService(t)
	submit(t, svc, "wabc123def456", controlled(transURI), TypeAdd, "h1")
	sub := controlled("https://homosaurus.org/v4/homoit0000508")
	_, err := svc.Review(t.Context(), []Decision{{
		WorkID: "wabc123def456", Term: controlled(transURI), Type: TypeAdd,
		Approve: true, SubstituteTerm: &sub,
	}}, "lib")
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	items, _ := svc.ForWork(t.Context(), "wabc123def456")
	if items[0].SubstituteTerm == nil || items[0].SubstituteTerm.ID != sub.ID {
		t.Fatalf("substitute = %+v", items[0])
	}
	// Unknown substitute rejected.
	bad := vocab.TermRef{Scheme: "homosaurus", ID: "https://homosaurus.org/v4/nope"}
	submit(t, svc, "wzzz999zzz999", controlled(transURI), TypeAdd, "h2")
	_, err = svc.Review(t.Context(), []Decision{{
		WorkID: "wzzz999zzz999", Term: controlled(transURI), Type: TypeAdd,
		Approve: true, SubstituteTerm: &bad,
	}}, "lib")
	if !errors.Is(err, ErrBadTerm) {
		t.Fatalf("bad substitute: %v", err)
	}
}

func TestManualTermAndPublishList(t *testing.T) {
	svc, _ := newService(t)
	if err := svc.ManualTerm(t.Context(), "wabc123def456", controlled(transURI), "A Book", "lib"); err != nil {
		t.Fatalf("ManualTerm: %v", err)
	}
	// Duplicate manual add conflicts.
	if err := svc.ManualTerm(t.Context(), "wabc123def456", controlled(transURI), "", "lib"); err == nil {
		t.Fatal("duplicate manual term accepted")
	}
	// Born approved -> in the publisher worklist.
	pending, err := svc.ApprovedUnpublished(t.Context())
	if err != nil || len(pending) != 1 || pending[0].Provenance != ProvenanceLibrarian {
		t.Fatalf("worklist = %+v, %v", pending, err)
	}
	// Publishing stamps and removes from the worklist.
	if err := svc.MarkPublished(t.Context(), pending, "etag-123"); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	pending, _ = svc.ApprovedUnpublished(t.Context())
	if len(pending) != 0 {
		t.Fatalf("worklist after publish = %+v", pending)
	}
	items, _ := svc.ForWork(t.Context(), "wabc123def456")
	if items[0].PublishedETag != "etag-123" {
		t.Fatalf("published stamp = %+v", items[0])
	}
}

func TestFolkGovernance(t *testing.T) {
	svc, _ := newService(t)
	submit(t, svc, "wabc123def456", folk("found family"), TypeAdd, "h1")
	// Accept: joins the autocomplete set.
	if err := svc.SetFolkStatus(t.Context(), "found family", FolkAccepted, "lib"); err != nil {
		t.Fatal(err)
	}
	names, err := svc.AcceptedFolkTerms(t.Context(), "fou", 10)
	if err != nil || len(names) != 1 || names[0] != "found family" {
		t.Fatalf("accepted terms = %v, %v", names, err)
	}
	// Block: leaves autocomplete, refuses submissions.
	if err := svc.SetFolkStatus(t.Context(), "found family", FolkBlocked, "lib"); err != nil {
		t.Fatal(err)
	}
	names, _ = svc.AcceptedFolkTerms(t.Context(), "", 10)
	if len(names) != 0 {
		t.Fatalf("blocked term still listed: %v", names)
	}
	if _, err := svc.Submit(t.Context(), SubmitInput{
		WorkID: "wzzz999zzz999", Term: folk("found family"), Type: TypeAdd, SupporterHash: "h2",
	}); !errors.Is(err, ErrFolkBlocked) {
		t.Fatalf("blocked submit: %v", err)
	}
	// Unknown term / invalid status rejected.
	if err := svc.SetFolkStatus(t.Context(), "never seen", FolkAccepted, "lib"); err == nil {
		t.Fatal("unknown folk term accepted")
	}
	if err := svc.SetFolkStatus(t.Context(), "found family", FolkProposed, "lib"); err == nil {
		t.Fatal("demoting to PROPOSED allowed")
	}
}

func TestQueuePagination(t *testing.T) {
	svc, _ := newService(t)
	for i := range 5 {
		submit(t, svc, workIDN(i), controlled(transURI), TypeAdd, "h1")
	}
	page1, err := svc.Queue(t.Context(), QueueQuery{Limit: 2})
	if err != nil || len(page1.Items) != 2 || page1.Cursor == "" {
		t.Fatalf("page1 = %+v, %v", page1, err)
	}
	page2, err := svc.Queue(t.Context(), QueueQuery{Limit: 10, Cursor: page1.Cursor})
	if err != nil || len(page2.Items) != 3 || page2.Cursor != "" {
		t.Fatalf("page2 = %+v, %v", page2, err)
	}
	// The triage denominator (task 445): Total is the whole filtered set,
	// the same on every page regardless of cursor.
	if page1.Total != 5 || page2.Total != 5 {
		t.Fatalf("totals = %d/%d, want 5 on both pages", page1.Total, page2.Total)
	}
}

// TestQueueTotalRespectsFilters pins that the denominator counts what
// triage will actually encounter: the same filters and confidence floor
// as the rows (task 445).
func TestQueueTotalRespectsFilters(t *testing.T) {
	svc, _ := newService(t)
	submit(t, svc, workIDN(0), controlled(transURI), TypeAdd, "h1")
	submit(t, svc, workIDN(1), folk("cozy vibes"), TypeAdd, "h2")
	if err := svc.PipelineSuggest(t.Context(), workIDN(2), controlled(transURI), 0.4); err != nil {
		t.Fatal(err)
	}

	all, err := svc.Queue(t.Context(), QueueQuery{})
	if err != nil || all.Total != 3 {
		t.Fatalf("unfiltered total = %d, %v; want 3", all.Total, err)
	}
	floored, err := svc.Queue(t.Context(), QueueQuery{MinConfidence: 0.6})
	if err != nil || floored.Total != 2 || len(floored.Items) != 2 {
		t.Fatalf("floored = total %d, %d items, %v; want 2/2 (the 0.4 pipeline row hidden)", floored.Total, len(floored.Items), err)
	}
	scheme, err := svc.Queue(t.Context(), QueueQuery{Scheme: "homosaurus"})
	if err != nil || scheme.Total != 2 {
		t.Fatalf("scheme total = %d, %v; want 2", scheme.Total, err)
	}
}

func workIDN(i int) string {
	return "wabc123def45" + string(rune('a'+i))
}
