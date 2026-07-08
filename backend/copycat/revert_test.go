package copycat_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/copycat"
	"github.com/freeeve/libcat/backend/marcview"
)

// docsWithTitle copies the staged records' field arrays with 245$a replaced:
// the provider identifiers stay put, so a second commit overlays the same
// identity with different feed statements.
func docsWithTitle(records []copycat.StagedRecord, title string) []marcview.RecordDoc {
	docs := make([]marcview.RecordDoc, len(records))
	for i, sr := range records {
		doc := sr.Record
		fields := make([]marcview.Field, len(doc.Fields))
		copy(fields, doc.Fields)
		for fi, f := range fields {
			if f.Tag != "245" {
				continue
			}
			subs := make([]marcview.Subfield, len(f.Subfields))
			copy(subs, f.Subfields)
			for si, sf := range subs {
				if sf.Code == "a" {
					subs[si].Value = title
				}
			}
			fields[fi].Subfields = subs
		}
		doc.Fields = fields
		docs[i] = doc
	}
	return docs
}

func TestRevertTombstonesCreatedWorks(t *testing.T) {
	svc, bs, _ := newService(t)
	ctx := t.Context()
	batch, _, err := svc.StageMARC(ctx, "load", sampleMRC(t), "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	committed, err := svc.Commit(ctx, batch.ID, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Revert(ctx, committed.ID, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if result.Batch.Status != copycat.StatusReverted || result.Reverted == 0 || len(result.Skipped) != 0 {
		t.Fatalf("revert = %+v", result)
	}
	// Every created grain is tombstoned, not deleted.
	for entry, err := range bs.List(ctx, "data/works/") {
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(entry.Path, ".nq") {
			continue
		}
		grain, _, err := bs.Get(ctx, entry.Path)
		if err != nil {
			t.Fatal(err)
		}
		workID := strings.TrimSuffix(entry.Path[strings.LastIndex(entry.Path, "/")+1:], ".nq")
		vis, err := bibframe.Visibility(grain, workID)
		if err != nil {
			t.Fatal(err)
		}
		if !vis.Tombstoned {
			t.Fatalf("created work %s not tombstoned after revert", workID)
		}
	}
	// A second revert refuses: the batch is no longer COMMITTED.
	if _, err := svc.Revert(ctx, committed.ID, "lib@example.org"); !errors.Is(err, copycat.ErrValidation) {
		t.Fatalf("double revert err = %v", err)
	}
}

func TestRevertRestoresOverlayBytesAndSkipsEdited(t *testing.T) {
	svc, bs, _ := newService(t)
	ctx := t.Context()
	// First commit creates the work.
	batch1, records, err := svc.StageMARC(ctx, "load", sampleMRC(t), "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Commit(ctx, batch1.ID, "lib@example.org"); err != nil {
		t.Fatal(err)
	}
	// Locate the grain and snapshot its post-commit-1 bytes.
	var grainPath string
	for entry, err := range bs.List(ctx, "data/works/") {
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(entry.Path, ".nq") {
			grainPath = entry.Path
		}
	}
	before, _, err := bs.Get(ctx, grainPath)
	if err != nil {
		t.Fatal(err)
	}

	// Second batch: same identifiers, retitled 245$a -> overlay.
	batch2, _, err := svc.Stage(ctx, "overlay", "upload", docsWithTitle(records, "A Different Title"), "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Commit(ctx, batch2.ID, "lib@example.org"); err != nil {
		t.Fatal(err)
	}
	after, _, err := bs.Get(ctx, grainPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Fatal("overlay commit did not change the grain")
	}

	// Revert restores the prior bytes exactly (every overlaid grain).
	result, err := svc.Revert(ctx, batch2.ID, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if result.Reverted == 0 || len(result.Skipped) != 0 {
		t.Fatalf("revert = %+v", result)
	}
	total := result.Reverted
	restored, _, err := bs.Get(ctx, grainPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(before) {
		t.Fatal("revert did not restore prior bytes byte-exactly")
	}

	// Re-commit the overlay, then edit the grain -- the revert must skip it.
	if _, err := svc.Commit(ctx, batch2.ID, "lib@example.org"); err != nil {
		t.Fatal(err)
	}
	grain, _, err := bs.Get(ctx, grainPath)
	if err != nil {
		t.Fatal(err)
	}
	workID := strings.TrimSuffix(grainPath[strings.LastIndex(grainPath, "/")+1:], ".nq")
	edited, err := bibframe.ApplyEditorialPatch(grain, bibframe.Patch{
		Add: []rdf.Quad{bibframe.TagQuad(workID, "post-commit-edit")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(ctx, grainPath, edited, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err = svc.Revert(ctx, batch2.ID, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if result.Reverted != total-1 || len(result.Skipped) != 1 || result.Skipped[0].Reason != "edited after commit" {
		t.Fatalf("revert after edit = %+v", result)
	}
	current, _, _ := bs.Get(ctx, grainPath)
	if !strings.Contains(string(current), "post-commit-edit") {
		t.Fatal("revert destroyed the editorial edit")
	}
	// A staged (never committed) batch refuses to revert.
	batch3, _, err := svc.StageMARC(ctx, "staged only", sampleMRC(t), "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Revert(ctx, batch3.ID, "lib@example.org"); !errors.Is(err, copycat.ErrValidation) {
		t.Fatalf("revert staged err = %v", err)
	}
}

func TestProfilesCRUD(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := t.Context()
	if err := svc.PutProfile(ctx, copycat.Profile{Name: "weekly-loc", Targets: []string{"loc"}, Policy: copycat.PolicyFillHoles}); err != nil {
		t.Fatal(err)
	}
	if err := svc.PutProfile(ctx, copycat.Profile{Name: "", Policy: copycat.PolicyNever}); !errors.Is(err, copycat.ErrValidation) {
		t.Fatalf("unnamed profile err = %v", err)
	}
	if err := svc.PutProfile(ctx, copycat.Profile{Name: "x", Policy: "sideways"}); !errors.Is(err, copycat.ErrValidation) {
		t.Fatalf("bad policy err = %v", err)
	}
	profiles, err := svc.Profiles(ctx)
	if err != nil || len(profiles) != 1 || profiles[0].Name != "weekly-loc" || profiles[0].Policy != copycat.PolicyFillHoles {
		t.Fatalf("profiles = %+v, %v", profiles, err)
	}
	if err := svc.DeleteProfile(ctx, "weekly-loc"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteProfile(ctx, "weekly-loc"); !errors.Is(err, copycat.ErrNotFound) {
		t.Fatalf("double delete err = %v", err)
	}
}
