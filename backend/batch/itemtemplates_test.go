package batch_test

import (
	"errors"
	"testing"

	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/store"
)

func TestItemTemplatesCRUDAndSharing(t *testing.T) {
	svc := &batch.Service{DB: store.NewMem()}
	ctx := t.Context()

	mine, err := svc.CreateItemTemplate(ctx, batch.ItemTemplate{
		OwnedMeta: batch.OwnedMeta{Label: "Paperback"}, CallNumber: "FIC", Location: "Main", BarcodePrefix: "B-",
	}, "eve@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if mine.ID == "" || mine.Owner != "eve@example.org" || mine.Shared {
		t.Fatalf("created = %+v", mine)
	}
	shared, err := svc.CreateItemTemplate(ctx, batch.ItemTemplate{
		OwnedMeta: batch.OwnedMeta{Label: "Branch copy", Shared: true},
		Location:  "Branch", BarcodePrefix: "BR-", BarcodeWidth: 6,
	}, "amy@example.org")
	if err != nil {
		t.Fatal(err)
	}

	// Everyone sees their own plus every shared template.
	list, err := svc.ListItemTemplates(ctx, "eve@example.org")
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	// A shared template resolves for a non-owner...
	got, err := svc.GetItemTemplate(ctx, "eve@example.org", shared.ID)
	if err != nil || got.Label != "Branch copy" {
		t.Fatalf("get shared = %+v, %v", got, err)
	}
	// ...a non-owner, non-admin librarian may not update or delete it -- the
	// owner-gating rule, kept for personal records (tasks/292).
	if _, err := svc.UpdateItemTemplate(ctx, shared.ID, shared, "eve@example.org", false); !errors.Is(err, batch.ErrForbidden) {
		t.Fatalf("non-owner update err = %v", err)
	}
	if err := svc.DeleteItemTemplate(ctx, "eve@example.org", shared.ID, false); !errors.Is(err, batch.ErrForbidden) {
		t.Fatalf("non-owner delete err = %v", err)
	}
	// An admin is the custodian of a shared record: it may relabel it, and the
	// relabel is not allowed to un-share it back into the owner's partition
	// (which would re-orphan it).
	relabel := shared
	relabel.Label = "Branch copy (curated)"
	relabel.Shared = false // an admin custodian's attempt to un-share is ignored
	curated, err := svc.UpdateItemTemplate(ctx, shared.ID, relabel, "boss@example.org", true)
	if err != nil || curated.Label != "Branch copy (curated)" || !curated.Shared || curated.Owner != "amy@example.org" {
		t.Fatalf("admin relabel = %+v, %v", curated, err)
	}
	// A personal record stays private even from an admin: invisible outside its
	// owner's partition, it cannot be reached at all.
	if err := svc.DeleteItemTemplate(ctx, "boss@example.org", mine.ID, true); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("admin delete of a personal record err = %v, want NotFound", err)
	}
	// An admin may delete a shared record outright -- the recovery path for a
	// row whose owner's account is gone.
	if err := svc.DeleteItemTemplate(ctx, "boss@example.org", shared.ID, true); err != nil {
		t.Fatalf("admin delete of shared template: %v", err)
	}
	if _, err := svc.GetItemTemplate(ctx, "amy@example.org", shared.ID); !errors.Is(err, batch.ErrNotFound) {
		t.Fatalf("shared template still present after admin delete: %v", err)
	}

	// Flipping Shared moves the record between partitions (the owner's own act).
	mine.Shared = true
	updated, err := svc.UpdateItemTemplate(ctx, mine.ID, mine, "eve@example.org", false)
	if err != nil || !updated.Shared {
		t.Fatalf("share flip = %+v, %v", updated, err)
	}
	list, _ = svc.ListItemTemplates(ctx, "amy@example.org")
	if len(list) != 1 {
		t.Fatalf("amy sees %d templates, want 1 (eve's shared; amy's was admin-deleted)", len(list))
	}
	if err := svc.DeleteItemTemplate(ctx, "eve@example.org", mine.ID, false); err != nil {
		t.Fatal(err)
	}

	// Validation floor.
	if _, err := svc.CreateItemTemplate(ctx, batch.ItemTemplate{}, "eve@example.org"); !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("unlabeled template err = %v", err)
	}
	if _, err := svc.CreateItemTemplate(ctx, batch.ItemTemplate{OwnedMeta: batch.OwnedMeta{Label: "x"}, BarcodeWidth: 44}, "eve@example.org"); !errors.Is(err, batch.ErrValidation) {
		t.Fatalf("wide barcode err = %v", err)
	}
}
