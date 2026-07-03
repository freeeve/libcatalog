package copycat_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	codex "github.com/freeeve/libcodex"
	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/copycat"
	"github.com/freeeve/libcatalog/backend/store"
	"github.com/freeeve/libcatalog/backend/trigger"
)

type fakeNotifier struct{ events []trigger.Event }

func (f *fakeNotifier) Notify(_ context.Context, e trigger.Event) error {
	f.events = append(f.events, e)
	return nil
}

func sampleMRC(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("../../ingest/overdrive/testdata/marc-express/od-sample-ebook.mrc")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func newService(t *testing.T) (*copycat.Service, blob.Store, *fakeNotifier) {
	t.Helper()
	notifier := &fakeNotifier{}
	svc := &copycat.Service{
		Blob: blob.NewMem(), DB: store.NewMem(), Trigger: notifier,
	}
	return svc, svc.Blob, notifier
}

func TestTargetsCRUD(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := t.Context()
	if err := svc.PutTarget(ctx, copycat.Target{Name: "loc", URL: "http://lx2.loc.gov:210/LCDB", Protocol: "sru"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.PutTarget(ctx, copycat.Target{Name: "bad"}); !errors.Is(err, copycat.ErrValidation) {
		t.Fatalf("bad target err = %v", err)
	}
	targets, err := svc.Targets(ctx)
	if err != nil || len(targets) != 1 || targets[0].Name != "loc" {
		t.Fatalf("targets = %+v, %v", targets, err)
	}
	if err := svc.DeleteTarget(ctx, "loc"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteTarget(ctx, "loc"); !errors.Is(err, copycat.ErrNotFound) {
		t.Fatalf("double delete err = %v", err)
	}
}

func TestSearchFanOut(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := t.Context()
	for _, name := range []string{"alpha", "beta"} {
		if err := svc.PutTarget(ctx, copycat.Target{Name: name, URL: "x", Protocol: "sru"}); err != nil {
			t.Fatal(err)
		}
	}
	svc.Search = func(_ context.Context, tgt copycat.Target, query string, _ int) ([]*codex.Record, error) {
		if tgt.Name == "beta" {
			return nil, errors.New("connection refused")
		}
		rec := codex.NewRecord()
		rec.AddField(codex.NewControlField("001", "X1"))
		rec.AddField(codex.NewDataField("245", '1', '0', codex.NewSubfield('a', "Hit for "+query)))
		rec.AddField(codex.NewDataField("020", ' ', ' ', codex.NewSubfield('a', "9781250313195")))
		return []*codex.Record{rec}, nil
	}
	results, failures, err := svc.SearchAll(ctx, "gideon", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Target != "alpha" || results[0].Title != "Hit for gideon" || results[0].ISBN != "9781250313195" {
		t.Fatalf("results = %+v", results)
	}
	if failures["beta"] == "" {
		t.Fatalf("failures = %v", failures)
	}
	if _, _, err := svc.SearchAll(ctx, "", nil); !errors.Is(err, copycat.ErrValidation) {
		t.Fatalf("empty query err = %v", err)
	}
}

// TestStageCommitLifecycle is the tasks/050 acceptance: stage a .mrc batch,
// review matches, commit through the shared pipeline, and re-commit
// byte-stable.
func TestStageCommitLifecycle(t *testing.T) {
	svc, bs, notifier := newService(t)
	ctx := t.Context()
	mrc := sampleMRC(t)

	// Stage: fresh corpus, so nothing matches.
	batch, records, err := svc.StageMARC(ctx, "first load", mrc, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if batch.Status != copycat.StatusStaged || batch.Records != len(records) || len(records) == 0 {
		t.Fatalf("batch = %+v (%d records)", batch, len(records))
	}
	if records[0].Match.MatchedWork || records[0].Match.MatchedInstance {
		t.Fatalf("fresh corpus matched: %+v", records[0].Match)
	}
	if records[0].Title == "" {
		t.Fatal("staged record missing title")
	}

	// Commit: grains land through the shared identity/cluster pipeline.
	committed, err := svc.Commit(ctx, batch.ID, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if committed.Status != copycat.StatusCommitted || committed.Committed != len(records) || committed.Skipped != 0 {
		t.Fatalf("committed = %+v", committed)
	}
	if len(notifier.events) != 1 || len(notifier.events[0].Paths) == 0 {
		t.Fatalf("events = %+v", notifier.events)
	}
	grainPath := notifier.events[0].Paths[0]
	grain1, _, err := bs.Get(ctx, grainPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(grain1), "feed:copycat") {
		t.Fatalf("grain not under the copycat feed:\n%.300s", grain1)
	}
	// The verbatim sidecar rode through the shared pipeline (tasks/049).
	if !strings.Contains(string(grain1), bibframe.PredMARCVerbatim) {
		t.Fatal("verbatim sidecar missing from committed grain")
	}

	// An editorial statement written after the commit must survive a
	// re-commit (the pipeline preserves the editorial graph).
	workID := strings.TrimSuffix(grainPath[strings.LastIndex(grainPath, "/")+1:], ".nq")
	withTag, err := bibframe.ApplyEditorialPatch(grain1, bibframe.Patch{
		Add: []rdf.Quad{bibframe.TagQuad(workID, "copycat-kept-tag")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bs.Put(ctx, grainPath, withTag, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	grain1 = withTag

	// Re-staging the same file now shows the match banner.
	batch2, records2, err := svc.StageMARC(ctx, "same again", mrc, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if !records2[0].Match.MatchedWork || !records2[0].Match.MatchedInstance {
		t.Fatalf("re-stage did not match: %+v", records2[0].Match)
	}
	if records2[0].Match.WorkID == "" || !strings.HasPrefix(records2[0].Match.WorkID, "w") {
		t.Fatalf("match ids = %+v", records2[0].Match)
	}

	// Re-commit is byte-stable: same ids, same bytes, no grains-changed
	// paths (unchanged grains are not rewritten).
	notifier.events = nil
	recommitted, err := svc.Commit(ctx, batch2.ID, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if recommitted.Committed != len(records2) {
		t.Fatalf("recommit = %+v", recommitted)
	}
	grain2, _, err := bs.Get(ctx, grainPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(grain1) != string(grain2) {
		t.Fatal("re-commit changed the grain bytes")
	}
	if !strings.Contains(string(grain2), "copycat-kept-tag") {
		t.Fatal("editorial statement lost on re-commit")
	}
	if len(notifier.events) != 0 {
		t.Fatalf("byte-stable recommit still notified: %+v", notifier.events)
	}
}

func TestPoliciesAndDecisions(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := t.Context()
	mrc := sampleMRC(t)

	// First commit seeds the corpus.
	b1, _, err := svc.StageMARC(ctx, "seed", mrc, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Commit(ctx, b1.ID, "lib@example.org"); err != nil {
		t.Fatal(err)
	}

	// PolicyNever: everything matches now, so everything is skipped.
	b2, _, err := svc.StageMARC(ctx, "again", mrc, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Review(ctx, b2.ID, copycat.PolicyNever, nil); err != nil {
		t.Fatal(err)
	}
	done, err := svc.Commit(ctx, b2.ID, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if done.Committed != 0 || done.Skipped != done.Records {
		t.Fatalf("never policy = %+v", done)
	}

	// PolicyFillHoles skips matched instances the same way here (the sample
	// batch re-imports the identical instance).
	b3, _, err := svc.StageMARC(ctx, "fill", mrc, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Review(ctx, b3.ID, copycat.PolicyFillHoles, nil); err != nil {
		t.Fatal(err)
	}
	done, err = svc.Commit(ctx, b3.ID, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if done.Committed != 0 || done.Skipped != done.Records {
		t.Fatalf("fill-holes policy = %+v", done)
	}

	// Per-record skip decision.
	b4, recs4, err := svc.StageMARC(ctx, "skip one", mrc, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Review(ctx, b4.ID, "", map[int]string{0: copycat.DecisionSkip}); err != nil {
		t.Fatal(err)
	}
	done, err = svc.Commit(ctx, b4.ID, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if done.Skipped < 1 || done.Committed != len(recs4)-1 {
		t.Fatalf("decision skip = %+v", done)
	}

	// Review validation.
	if _, err := svc.Review(ctx, b4.ID, "bogus", nil); !errors.Is(err, copycat.ErrValidation) {
		t.Fatalf("bad policy err = %v", err)
	}
	if _, _, err := svc.GetBatch(ctx, "missing"); !errors.Is(err, copycat.ErrNotFound) {
		t.Fatalf("missing batch err = %v", err)
	}
}
