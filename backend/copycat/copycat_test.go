package copycat_test

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	codex "github.com/freeeve/libcodex"
	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/copycat"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/trigger"
	"github.com/freeeve/libcat/backend/workindex"
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

// newService builds a copycat service wired the way appdeps does: with the
// shared work index over the same store, so the suite exercises the indexed
// match path. Fallback-specific tests zero the Index field.
func newService(t *testing.T) (*copycat.Service, blob.Store, *fakeNotifier) {
	t.Helper()
	notifier := &fakeNotifier{}
	svc := &copycat.Service{
		Blob: blob.NewMem(), DB: store.NewMem(), Trigger: notifier,
	}
	svc.Index = workindex.New(svc.Blob, "data/works/")
	return svc, svc.Blob, notifier
}

// TestSeedDefaultTargets proves a virgin store gets every default SRU target
// once ever: re-seeding after an admin deletes everything stays at zero, and
// a store that already has targets is never touched.
func TestSeedDefaultTargets(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := t.Context()
	if err := svc.SeedDefaultTargets(ctx); err != nil {
		t.Fatal(err)
	}
	targets, err := svc.Targets(ctx)
	if err != nil || len(targets) != len(copycat.DefaultTargets) {
		t.Fatalf("seeded targets = %+v, %v", targets, err)
	}
	for i, want := range copycat.DefaultTargets {
		got := targets[i]
		if got.Name != want.Name || got.URL != want.URL || got.Protocol != want.Protocol ||
			got.Version != want.Version || got.Schema != want.Schema || len(got.Indexes) != len(want.Indexes) {
			t.Fatalf("seeded target %d = %+v, want %+v", i, got, want)
		}
	}
	for _, want := range copycat.DefaultTargets {
		if err := svc.DeleteTarget(ctx, want.Name); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.SeedDefaultTargets(ctx); err != nil {
		t.Fatal(err)
	}
	if targets, _ = svc.Targets(ctx); len(targets) != 0 {
		t.Fatalf("re-seed after delete = %+v", targets)
	}

	svc2, _, _ := newService(t)
	if err := svc2.PutTarget(ctx, copycat.Target{Name: "mine", URL: "http://example.org/sru", Protocol: "sru"}); err != nil {
		t.Fatal(err)
	}
	if err := svc2.SeedDefaultTargets(ctx); err != nil {
		t.Fatal(err)
	}
	if targets, _ = svc2.Targets(ctx); len(targets) != 1 || targets[0].Name != "mine" {
		t.Fatalf("seed over existing = %+v", targets)
	}
}

// TestSuggestedTargetsAgreeWithDefaults is the drift guard: a one-click
// preset that points at the same URL as a seeded default must carry the same SRU
// knobs, or the preset speaks different CQL than the seeded target for the same
// server. It is exactly this that let the k10plus preset ship without the PICA
// indexes its k10plus-sru twin carries.
func TestSuggestedTargetsAgreeWithDefaults(t *testing.T) {
	byURL := map[string]copycat.Target{}
	for _, d := range copycat.DefaultTargets {
		byURL[d.URL] = d
	}
	for _, s := range copycat.SuggestedTargets {
		d, ok := byURL[s.URL]
		if !ok {
			continue // a preset for a server not among the seeded defaults
		}
		if s.Version != d.Version || s.Schema != d.Schema || !maps.Equal(s.Indexes, d.Indexes) {
			t.Errorf("suggested %q and default %q share URL %s but disagree on SRU knobs:\n suggested: v=%q schema=%q indexes=%v\n default:   v=%q schema=%q indexes=%v",
				s.Name, d.Name, s.URL, s.Version, s.Schema, s.Indexes, d.Version, d.Schema, d.Indexes)
		}
	}
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
	svc.Search = func(_ context.Context, tgt copycat.Target, terms []copycat.FieldTerm, _ int) ([]*codex.Record, error) {
		if tgt.Name == "beta" {
			return nil, errors.New("connection refused")
		}
		rec := codex.NewRecord()
		rec.AddField(codex.NewControlField("001", "X1"))
		rec.AddField(codex.NewDataField("245", '1', '0', codex.NewSubfield('a', "Hit for "+terms[0].Term)))
		rec.AddField(codex.NewDataField("020", ' ', ' ', codex.NewSubfield('a', "9781250313195")))
		rec.AddField(codex.NewDataField("250", ' ', ' ', codex.NewSubfield('a', "1st ed.")))
		rec.AddField(codex.NewDataField("010", ' ', ' ', codex.NewSubfield('a', "2019978000")))
		return []*codex.Record{rec}, nil
	}
	results, failures, warnings, err := svc.SearchAll(ctx, "gideon", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none: both streams ended cleanly", warnings)
	}
	if len(results) != 1 || results[0].Target != "alpha" || results[0].Title != "Hit for gideon" || results[0].ISBN != "9781250313195" {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Edition != "1st ed." || results[0].LCCN != "2019978000" {
		t.Fatalf("edition/lccn = %+v", results[0])
	}
	if failures["beta"] == "" {
		t.Fatalf("failures = %v", failures)
	}
	if _, _, _, err := svc.SearchAll(ctx, "", nil, nil); !errors.Is(err, copycat.ErrValidation) {
		t.Fatalf("empty query err = %v", err)
	}
}

// TestSearchFielded is the access-point path: fields AND onto the
// free-text query and reach the protocol seam in order; bad indexes and
// empty terms are refused.
func TestSearchFielded(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := t.Context()
	if err := svc.PutTarget(ctx, copycat.Target{Name: "alpha", URL: "x", Protocol: "sru"}); err != nil {
		t.Fatal(err)
	}
	var got []copycat.FieldTerm
	svc.Search = func(_ context.Context, _ copycat.Target, terms []copycat.FieldTerm, _ int) ([]*codex.Record, error) {
		got = terms
		return nil, nil
	}
	fields := []copycat.FieldTerm{{Index: "isbn", Term: "9780062963673"}, {Index: "author", Term: "patchett"}}
	if _, _, _, err := svc.SearchAll(ctx, "dutch house", fields, nil); err != nil {
		t.Fatal(err)
	}
	want := append([]copycat.FieldTerm{{Index: "any", Term: "dutch house"}}, fields...)
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("terms = %+v", got)
	}
	// Fields alone (no free-text query) are a valid search.
	if _, _, _, err := svc.SearchAll(ctx, "", fields[:1], nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := svc.SearchAll(ctx, "", []copycat.FieldTerm{{Index: "dewey", Term: "813"}}, nil); !errors.Is(err, copycat.ErrValidation) {
		t.Fatalf("unknown index err = %v", err)
	}
	if _, _, _, err := svc.SearchAll(ctx, "", []copycat.FieldTerm{{Index: "title", Term: ""}}, nil); !errors.Is(err, copycat.ErrValidation) {
		t.Fatalf("empty term err = %v", err)
	}
}

// TestStageCommitLifecycle is the acceptance: stage a .mrc batch,
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
	// The verbatim sidecar rode through the shared pipeline.
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

// TestCommitAuditsPerWorkProvenance proves a committed batch writes not just
// the run-summary audit entry but one COPYCAT_COMMIT entry per committed work,
// carrying that work's ID -- so an imported record's History tab shows where it
// came from. Before the commit audit named the batch totals only, so
// a work's history (which filters on WorkID) showed nothing about its import.
func TestCommitAuditsPerWorkProvenance(t *testing.T) {
	notifier := &fakeNotifier{}
	queue := suggest.New(store.NewMem(), nil, suggest.Caps{})
	svc := &copycat.Service{
		Blob: blob.NewMem(), DB: store.NewMem(), Trigger: notifier, Queue: queue,
	}
	svc.Index = workindex.New(svc.Blob, "data/works/")
	ctx := t.Context()

	batch, records, err := svc.StageMARC(ctx, "prov load", sampleMRC(t), "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	committed, err := svc.Commit(ctx, batch.ID, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if committed.Committed != len(records) || len(records) == 0 {
		t.Fatalf("committed = %+v (%d records)", committed, len(records))
	}

	entries, err := queue.Audit(ctx, time.Now().UTC().Format("2006-01"))
	if err != nil {
		t.Fatal(err)
	}

	var summary, perWork []suggest.AuditEntry
	for _, e := range entries {
		if e.Action != "COPYCAT_COMMIT" {
			continue
		}
		if e.WorkID == "" {
			summary = append(summary, e)
		} else {
			perWork = append(perWork, e)
		}
	}
	if len(summary) != 1 {
		t.Fatalf("want one run-summary COPYCAT_COMMIT entry, got %d", len(summary))
	}
	if len(perWork) != committed.Committed {
		t.Fatalf("want %d per-work entries, got %d", committed.Committed, len(perWork))
	}
	for _, e := range perWork {
		if !strings.HasPrefix(e.WorkID, "w") {
			t.Errorf("per-work entry WorkID = %q, want a work id", e.WorkID)
		}
		if e.RunID != batch.ID || summary[0].RunID != batch.ID {
			t.Errorf("RunID mismatch: per-work %q, summary %q, batch %q", e.RunID, summary[0].RunID, batch.ID)
		}
		if e.Actor != "lib@example.org" {
			t.Errorf("per-work entry actor = %q", e.Actor)
		}
	}
}

// TestDeleteBatchRefusesCommittedUntilReverted pins the guard: a
// COMMITTED batch cannot be deleted, because its revert-set is the only undo for
// the works it created -- deleting it would strand them non-revertable. Reverting
// first (which sets REVERTED) frees it. STAGED batches delete freely.
func TestDeleteBatchRefusesCommittedUntilReverted(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := t.Context()
	mrc := sampleMRC(t)

	// A COMMITTED batch is refused, and the refusal is a conflict (409-mapped).
	batch, _, err := svc.StageMARC(ctx, "commit me", mrc, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Commit(ctx, batch.ID, "lib@example.org"); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteBatch(ctx, batch.ID); !errors.Is(err, copycat.ErrConflict) {
		t.Fatalf("delete of a COMMITTED batch = %v, want ErrConflict", err)
	}
	// The guard must not have been a delete-then-fail: the batch and its revert
	// path survive, so the commit is still undoable.
	if _, err := svc.Revert(ctx, batch.ID, "lib@example.org"); err != nil {
		t.Fatalf("revert after a refused delete: %v", err)
	}
	// Once REVERTED, it deletes freely.
	if err := svc.DeleteBatch(ctx, batch.ID); err != nil {
		t.Fatalf("delete of a REVERTED batch: %v", err)
	}

	// A STAGED batch is freely deletable -- the guard is specific to COMMITTED.
	staged, _, err := svc.StageMARC(ctx, "just staged", mrc, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteBatch(ctx, staged.ID); err != nil {
		t.Fatalf("delete of a STAGED batch: %v", err)
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

// countingStore wraps a Store and counts Get calls, so tests can assert the
// indexed match path stops re-reading the corpus per Stage.
type countingStore struct {
	blob.Store
	gets atomic.Int64
}

func (c *countingStore) Get(ctx context.Context, path string) ([]byte, string, error) {
	c.gets.Add(1)
	return c.Store.Get(ctx, path)
}

func (c *countingStore) List(ctx context.Context, prefix string) iter.Seq2[blob.Entry, error] {
	return c.Store.List(ctx, prefix)
}

// TestIndexedMatchEqualsFallbackAndSkipsCorpusReads proves the shared-index
// match pass returns the same banner as the LoadPriorStore fallback and that
// a warm re-stage costs zero grain Gets.
func TestIndexedMatchEqualsFallbackAndSkipsCorpusReads(t *testing.T) {
	svc, bs, _ := newService(t)
	ctx := t.Context()
	mrc := sampleMRC(t)
	seeded, _, err := svc.StageMARC(ctx, "seed", mrc, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Commit(ctx, seeded.ID, "lib@example.org"); err != nil {
		t.Fatal(err)
	}

	// The same corpus through the LoadPriorStore fallback (no index).
	fallback := &copycat.Service{Blob: bs, DB: store.NewMem(), Trigger: &fakeNotifier{}}
	_, viaScan, err := fallback.StageMARC(ctx, "again", mrc, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}

	// The indexed path over a counting wrapper: warm once, then a re-stage
	// must not read any grain.
	cs := &countingStore{Store: bs}
	indexed := &copycat.Service{
		Blob: cs, DB: store.NewMem(), Trigger: &fakeNotifier{},
		Index: workindex.New(cs, "data/works/"),
	}
	if err := indexed.Index.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	cs.gets.Store(0)
	_, viaIndex, err := indexed.StageMARC(ctx, "again", mrc, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if got := cs.gets.Load(); got != 0 {
		t.Fatalf("warm indexed stage read %d grains, want 0", got)
	}
	if len(viaIndex) != len(viaScan) {
		t.Fatalf("record counts diverge: %d vs %d", len(viaIndex), len(viaScan))
	}
	for i := range viaIndex {
		got, want := fmt.Sprintf("%+v", viaIndex[i].Match), fmt.Sprintf("%+v", viaScan[i].Match)
		if got != want {
			t.Fatalf("record %d match diverges: index %s, fallback %s", i, got, want)
		}
	}
}

// TestSeedDefaultTargetsConcurrent covers cold starts sharing one
// table race the seed; exactly one wins the create-only marker and the rest
// no-op, so the target set comes out exactly once regardless.
func TestSeedDefaultTargetsConcurrent(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := t.Context()
	const racers = 8
	errs := make(chan error, racers)
	start := make(chan struct{})
	for range racers {
		go func() {
			<-start
			errs <- svc.SeedDefaultTargets(ctx)
		}()
	}
	close(start)
	for range racers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	targets, err := svc.Targets(ctx)
	if err != nil || len(targets) != len(copycat.DefaultTargets) {
		t.Fatalf("targets after concurrent seed = %d, %v", len(targets), err)
	}
}
