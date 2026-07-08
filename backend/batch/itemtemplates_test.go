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
	// ...but only the owner may update or delete it.
	if _, err := svc.UpdateItemTemplate(ctx, shared.ID, shared, "eve@example.org"); !errors.Is(err, batch.ErrForbidden) {
		t.Fatalf("non-owner update err = %v", err)
	}
	if err := svc.DeleteItemTemplate(ctx, "eve@example.org", shared.ID); !errors.Is(err, batch.ErrForbidden) {
		t.Fatalf("non-owner delete err = %v", err)
	}

	// Flipping Shared moves the record between partitions.
	mine.Shared = true
	updated, err := svc.UpdateItemTemplate(ctx, mine.ID, mine, "eve@example.org")
	if err != nil || !updated.Shared {
		t.Fatalf("share flip = %+v, %v", updated, err)
	}
	list, _ = svc.ListItemTemplates(ctx, "amy@example.org")
	if len(list) != 2 {
		t.Fatalf("amy sees %d templates, want 2 (own shared + eve's shared)", len(list))
	}
	if err := svc.DeleteItemTemplate(ctx, "eve@example.org", mine.ID); err != nil {
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
