package suggest

import (
	"strings"
	"testing"
)

// TestConcernLifecycle covers an anonymous concern lands PENDING
// in the queue, resolves or dismisses through Review with legible audit
// actions, never reaches the publisher's worklist, and dedupes identical
// resubmissions.
func TestConcernLifecycle(t *testing.T) {
	svc, _ := newService(t)
	ctx := t.Context()

	// Bounds.
	if err := svc.SubmitConcern(ctx, "wconcern00001", "too short", "A Book", "h1"); err == nil {
		t.Fatal("short note accepted")
	}
	if err := svc.SubmitConcern(ctx, "wconcern00001", strings.Repeat("x", 2001), "A Book", "h1"); err == nil {
		t.Fatal("overlong note accepted")
	}

	note := "The summary describes a different edition entirely."
	if err := svc.SubmitConcern(ctx, "wconcern00001", note, "A Book", "h1"); err != nil {
		t.Fatal(err)
	}
	// Identical resubmission: idempotent no-op.
	if err := svc.SubmitConcern(ctx, "wconcern00001", note, "A Book", "h2"); err != nil {
		t.Fatal(err)
	}

	page, err := svc.Queue(ctx, QueueQuery{})
	if err != nil {
		t.Fatal(err)
	}
	var concern *Suggestion
	for i := range page.Items {
		if page.Items[i].Type == TypeConcern {
			if concern != nil {
				t.Fatal("duplicate concern queued")
			}
			concern = &page.Items[i]
		}
	}
	if concern == nil || concern.Note != note || concern.Term.Scheme != ConcernScheme {
		t.Fatalf("queued concern = %+v", concern)
	}

	// Resolve (approve) -- and the publisher's worklist must not see it.
	_, err = svc.Review(ctx, []Decision{{
		WorkID: concern.WorkID, Term: concern.Term, Type: TypeConcern, Approve: true, Note: "fixed the summary",
	}}, "mod@example.org")
	if err != nil {
		t.Fatal(err)
	}
	unpub, err := svc.ApprovedUnpublished(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, sg := range unpub {
		if sg.Type == TypeConcern {
			t.Fatalf("resolved concern reached the publisher worklist: %+v", sg)
		}
	}

	// A second, different concern dismisses.
	if err := svc.SubmitConcern(ctx, "wconcern00001", "Cover image is for the wrong book.", "A Book", "h3"); err != nil {
		t.Fatal(err)
	}
	page, _ = svc.Queue(ctx, QueueQuery{})
	for i := range page.Items {
		if page.Items[i].Type != TypeConcern {
			continue
		}
		if _, err := svc.Review(ctx, []Decision{{
			WorkID: page.Items[i].WorkID, Term: page.Items[i].Term, Type: TypeConcern, Approve: false,
		}}, "mod@example.org"); err != nil {
			t.Fatal(err)
		}
	}
	page, _ = svc.Queue(ctx, QueueQuery{})
	for _, sg := range page.Items {
		if sg.Type == TypeConcern {
			t.Fatalf("concern still pending after dismiss: %+v", sg)
		}
	}
}
