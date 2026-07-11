package batch_test

import (
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/editor"
)

// seedShelvedWork writes a Work with one Instance and the given holdings, the
// shape a batch relocation runs against.
func seedShelvedWork(t *testing.T, st blob.Store, workID, instID, title string, items []bibframe.Item) {
	t.Helper()
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	inst := rdf.NewIRI(bibframe.InstanceIRI(instID))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	tnode := rdf.NewBlank("t0")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), tnode, feed)
	ds.Add(tnode, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral(title, "", ""), feed)
	ds.Add(work, rdf.NewIRI(bfNS+"hasInstance"), inst, feed)
	ds.Add(inst, rdf.NewIRI(bfNS+"instanceOf"), work, feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	nq, err = bibframe.SetItems(nq, instID, items)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

// The whole point of closing a shelving location across a
// selection, without opening each record's item panel. The guard is what makes
// it safe -- the Reference copy stays where it is.
func TestRunRelocatesItemsAcrossASelection(t *testing.T) {
	svc, st, _, notifier := newService(t)
	ctx := t.Context()
	seedShelvedWork(t, st, "wshelf000001", "ishelf000001", "Gideon the Ninth", []bibframe.Item{
		{Barcode: "31234", Location: "Stacks", CallNumber: "FIC MUI"},
		{Barcode: "31235", Location: "Reference", CallNumber: "FIC MUI"},
	})
	seedShelvedWork(t, st, "wshelf000002", "ishelf000002", "Harrow the Ninth", []bibframe.Item{
		{Barcode: "31236", Location: "Stacks", CallNumber: "FIC MUI"},
	})

	stacks := "Stacks"
	ops := []editor.Op{{
		Resource: editor.ResourceItems, Path: "location", Action: "set",
		Values: []editor.OpValue{{V: "Annex"}}, Where: &stacks,
	}}
	sel := batch.Selection{Kind: batch.KindIDs, IDs: []string{"wshelf000001", "wshelf000002"}}

	// Dry run: two copies move, the diff proves it, nothing is written.
	dry, err := svc.Run(ctx, sel, ops, true, "lib@example.org")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if dry.Applied != 2 || dry.Failed != 0 {
		t.Fatalf("dry = %+v", dry)
	}
	if dry.Added != 2 || dry.Removed != 2 {
		t.Fatalf("dry diff = +%d/-%d, want +2/-2 (one Stacks copy per work)", dry.Added, dry.Removed)
	}
	grain, _, err := st.Get(ctx, bibframe.GrainPath("wshelf000001"))
	if err != nil || strings.Contains(string(grain), "Annex") {
		t.Fatalf("dry run wrote: %v", err)
	}
	if len(notifier.events) != 0 {
		t.Fatalf("dry run notified: %+v", notifier.events)
	}

	// Execute.
	run, err := svc.Run(ctx, sel, ops, false, "lib@example.org")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Applied != 2 || run.Failed != 0 {
		t.Fatalf("run = %+v", run)
	}
	grain, _, err = st.Get(ctx, bibframe.GrainPath("wshelf000001"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := bibframe.ItemsOf(grain, "ishelf000001")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	byBarcode := map[string]bibframe.Item{}
	for _, it := range items {
		byBarcode[it.Barcode] = it
	}
	if got := byBarcode["31234"].Location; got != "Annex" {
		t.Fatalf("the Stacks copy is in %q, want Annex", got)
	}
	if got := byBarcode["31235"].Location; got != "Reference" {
		t.Fatalf("the Reference copy moved to %q; the guard failed", got)
	}
	if got := byBarcode["31234"].CallNumber; got != "FIC MUI" {
		t.Fatalf("relocation clobbered the call number: %q", got)
	}

	// Re-running is a no-op: nothing is left in Stacks, so nothing changes.
	again, err := svc.Run(ctx, sel, ops, true, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if again.Added != 0 || again.Removed != 0 {
		t.Fatalf("re-run diff = +%d/-%d, want a no-op", again.Added, again.Removed)
	}
}

// A work with no holdings is a normal member of a selection, not a failure:
// most works have none, and a relocation must not fail 90% of the catalog.
func TestRunItemOpsOnWorksWithoutItems(t *testing.T) {
	svc, _, _, _ := newService(t)
	ops := []editor.Op{{Resource: editor.ResourceItems, Path: "location", Action: "set", Values: []editor.OpValue{{V: "Annex"}}}}
	run, err := svc.Run(t.Context(), batch.Selection{Kind: batch.KindSearch, Query: "ninth"}, ops, true, "lib@example.org")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Matched != 2 || run.Applied != 2 || run.Failed != 0 {
		t.Fatalf("run = %+v", run)
	}
	if run.Added != 0 || run.Removed != 0 {
		t.Fatalf("itemless works produced a diff: +%d/-%d", run.Added, run.Removed)
	}
}
