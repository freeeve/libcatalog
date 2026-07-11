package bibframe

import (
	"errors"
	"testing"

	"github.com/freeeve/libcodex/rdf"
)

// itemEditGrain builds a two-instance grain whose five items span the cases an
// ItemEdit has to tell apart: two in Stacks, one already in Annex, one in
// Reference, and one on the second instance (a batch edit reaches every
// instance's holdings, not just the first).
func itemEditGrain(t *testing.T) []byte {
	t.Helper()
	ds := &rdf.Dataset{}
	feed := FeedGraph("test")
	bf := "http://id.loc.gov/ontologies/bibframe/"
	work := rdf.NewIRI(WorkIRI("w1"))
	inst1, inst2 := rdf.NewIRI(InstanceIRI("i1")), rdf.NewIRI(InstanceIRI("i2"))
	ds.Add(work, rdf.NewIRI(bf+"hasInstance"), inst1, feed)
	ds.Add(work, rdf.NewIRI(bf+"hasInstance"), inst2, feed)
	ds.Add(inst1, rdf.NewIRI(bf+"instanceOf"), work, feed)
	ds.Add(inst2, rdf.NewIRI(bf+"instanceOf"), work, feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	nq, err = SetItems(nq, "i1", []Item{
		{CallNumber: "FIC UNG", Location: "Stacks", Barcode: "31234"},
		{CallNumber: "FIC UNG", Location: "Stacks", Barcode: "31235", Note: "spine cracked"},
		{CallNumber: "FIC UNG", Location: "Annex", Barcode: "31236"},
		{CallNumber: "FIC UNG", Location: "Reference", Barcode: "31237"},
	})
	if err != nil {
		t.Fatal(err)
	}
	nq, err = SetItems(nq, "i2", []Item{{Location: "Stacks", Barcode: "31238"}})
	if err != nil {
		t.Fatal(err)
	}
	return nq
}

// locations reads every item's location across both instances, sorted, so a
// test can assert the shelf as a whole.
func locations(t *testing.T, grain []byte) []string {
	t.Helper()
	var out []string
	for _, inst := range []string{"i1", "i2"} {
		items, err := ItemsOf(grain, inst)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range items {
			out = append(out, it.Location)
		}
	}
	return out
}

// TestSetItemsRefusesDuplicateBarcodesInOneList pins the guard: two
// items in one wholesale PUT may not share a barcode (a barcode names one
// physical copy), the invariant the auto-assign path already holds. Empty
// barcodes are exempt -- several items may legitimately carry none.
func TestSetItemsRefusesDuplicateBarcodesInOneList(t *testing.T) {
	grain := itemEditGrain(t)

	if _, err := SetItems(grain, "i1", []Item{
		{CallNumber: "A", Barcode: "DUP-1"},
		{CallNumber: "B", Barcode: "DUP-1"},
	}); !errors.Is(err, ErrDuplicateBarcode) {
		t.Fatalf("duplicate barcode = %v, want ErrDuplicateBarcode", err)
	}

	// Control: distinct barcodes are fine, and several empty barcodes are not
	// treated as duplicates of each other.
	if _, err := SetItems(grain, "i1", []Item{
		{Barcode: "UNIQ-1"}, {Barcode: "UNIQ-2"}, {Barcode: ""}, {Barcode: ""},
	}); err != nil {
		t.Fatalf("distinct barcodes with empties rejected: %v", err)
	}
}

func TestItemEditRelocatesOnlyTheGuardedItems(t *testing.T) {
	grain := itemEditGrain(t)
	stacks := "Stacks"
	patch, touched, err := ItemEditPatch(grain, ItemEdit{Field: "location", Value: "Annex", Where: &stacks})
	if err != nil {
		t.Fatalf("ItemEditPatch: %v", err)
	}
	// Three items sit in Stacks (two on i1, one on i2). The Annex copy is
	// already there and the Reference copy is not addressed.
	if touched != 3 {
		t.Fatalf("touched = %d, want 3", touched)
	}
	if len(patch.Add) != 3 || len(patch.Remove) != 3 {
		t.Fatalf("patch = +%d/-%d, want +3/-3", len(patch.Add), len(patch.Remove))
	}
	out, err := ApplyEditorialPatch(grain, patch)
	if err != nil {
		t.Fatalf("ApplyEditorialPatch: %v", err)
	}
	got := locations(t, out)
	want := []string{"Annex", "Annex", "Annex", "Reference", "Annex"}
	if len(got) != len(want) {
		t.Fatalf("locations = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("locations = %v, want %v", got, want)
		}
	}
}

func TestItemEditSkipsItemsAlreadyHoldingTheValue(t *testing.T) {
	grain := itemEditGrain(t)
	// No guard: every item moves to Annex, except the one already there.
	patch, touched, err := ItemEditPatch(grain, ItemEdit{Field: "location", Value: "Annex"})
	if err != nil {
		t.Fatalf("ItemEditPatch: %v", err)
	}
	if touched != 4 {
		t.Fatalf("touched = %d, want 4 (five items, one already in Annex)", touched)
	}
	// Re-running the same edit is a no-op: the diff a batch reports is the
	// work it did, so an idempotent run must report nothing.
	out, err := ApplyEditorialPatch(grain, patch)
	if err != nil {
		t.Fatal(err)
	}
	again, touched, err := ItemEditPatch(out, ItemEdit{Field: "location", Value: "Annex"})
	if err != nil {
		t.Fatal(err)
	}
	if touched != 0 || len(again.Add) != 0 || len(again.Remove) != 0 {
		t.Fatalf("re-run touched %d items (+%d/-%d), want a no-op", touched, len(again.Add), len(again.Remove))
	}
}

func TestItemEditClearsAndFillsMissingFields(t *testing.T) {
	grain := itemEditGrain(t)
	// Clearing: only the one item carrying a note is touched.
	patch, touched, err := ItemEditPatch(grain, ItemEdit{Field: "note", Value: ""})
	if err != nil {
		t.Fatalf("ItemEditPatch: %v", err)
	}
	if touched != 1 || len(patch.Add) != 0 || len(patch.Remove) != 1 {
		t.Fatalf("clear note: touched %d (+%d/-%d), want 1 (+0/-1)", touched, len(patch.Add), len(patch.Remove))
	}
	// An absent field reads as "", so Where:"" addresses exactly the items
	// missing it -- i2's item has no call number, i1's four do.
	blank := ""
	patch, touched, err = ItemEditPatch(grain, ItemEdit{Field: "callNumber", Value: "FIC NEW", Where: &blank})
	if err != nil {
		t.Fatalf("ItemEditPatch: %v", err)
	}
	if touched != 1 || len(patch.Add) != 1 || len(patch.Remove) != 0 {
		t.Fatalf("fill blanks: touched %d (+%d/-%d), want 1 (+1/-0)", touched, len(patch.Add), len(patch.Remove))
	}
	out, err := ApplyEditorialPatch(grain, patch)
	if err != nil {
		t.Fatal(err)
	}
	items, err := ItemsOf(out, "i2")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].CallNumber != "FIC NEW" {
		t.Fatalf("i2 items = %+v, want one with callNumber FIC NEW", items)
	}
	// The instance that already had call numbers is untouched.
	items, err = ItemsOf(out, "i1")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.CallNumber != "FIC UNG" {
			t.Fatalf("i1 item %s callNumber = %q, want FIC UNG", it.ID, it.CallNumber)
		}
	}
}

func TestItemEditRefusesBarcodeAndUnknownFields(t *testing.T) {
	grain := itemEditGrain(t)
	for _, field := range []string{"barcode", "shelf", ""} {
		if _, _, err := ItemEditPatch(grain, ItemEdit{Field: field, Value: "x"}); !errors.Is(err, ErrNoSuchItemField) {
			t.Fatalf("field %q: err = %v, want ErrNoSuchItemField", field, err)
		}
	}
	// A barcode names one physical copy: batch-assigning it would mint
	// duplicates, so it is not merely unlisted, it is unreachable.
	for _, name := range ItemFieldNames() {
		if name == "barcode" {
			t.Fatal("barcode is batch-editable; it must not be")
		}
	}
}

func TestItemEditRefusesLineBreaks(t *testing.T) {
	grain := itemEditGrain(t)
	if _, _, err := ItemEditPatch(grain, ItemEdit{Field: "note", Value: "one\ntwo"}); err == nil {
		t.Fatal("a note carrying a line break was accepted")
	}
}

// A grain with no holdings is a normal case (most works have none), not an
// error: the batch reports zero touched and writes nothing.
func TestItemEditOnAWorkWithNoItems(t *testing.T) {
	ds := &rdf.Dataset{}
	ds.Add(rdf.NewIRI(WorkIRI("w1")), rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/hasInstance"), rdf.NewIRI(InstanceIRI("i1")), FeedGraph("test"))
	grain, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	patch, touched, err := ItemEditPatch(grain, ItemEdit{Field: "location", Value: "Annex"})
	if err != nil {
		t.Fatalf("ItemEditPatch: %v", err)
	}
	if touched != 0 || len(patch.Add) != 0 || len(patch.Remove) != 0 {
		t.Fatalf("touched %d (+%d/-%d), want a no-op", touched, len(patch.Add), len(patch.Remove))
	}
}
