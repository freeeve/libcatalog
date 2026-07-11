// Merge wrote the durable record of the retirement before doing the
// work it describes. `lcat:mergedInto` landed on the loser's grain, then the
// rewrite loop returned on the first failing Work -- leaving some works pointing
// at the winner, the rest at a retired heading, no audit entry, a discarded count,
// and `500 merge failed` on the wire.
//
// A merge is the widest authority write in the product and the one most likely to
// meet a store failure partway. Same family as (promotion stamped
// APPROVED before PromoteTag ran).
//
// Every check here carries a control: a healthy merge runs first, or the same
// assertion is made on the success path, because "the loser is not retired" is
// also what a merge that never started looks like.
package authoritiesvc_test

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/authoritiesvc"
	"github.com/freeeve/libcat/backend/publish"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

// failWorkWrite wraps a blob store and fails the Nth Work-grain write, the way a
// read-only shard does. Authority-grain writes always pass, so the marker is free
// to land -- which is the point: it must not.
type failWorkWrite struct {
	blob.Store
	after  int // allow this many work-grain writes, then fail
	writes int
	off    bool
}

var errShardReadOnly = errors.New("shard is read-only")

func (f *failWorkWrite) Put(ctx context.Context, path string, data []byte, opts blob.PutOptions) (string, error) {
	if !f.off && strings.HasPrefix(path, "data/works/") {
		f.writes++
		if f.writes > f.after {
			return "", errShardReadOnly
		}
	}
	return f.Store.Put(ctx, path, data, opts)
}
func (f *failWorkWrite) Get(ctx context.Context, p string) ([]byte, string, error) {
	return f.Store.Get(ctx, p)
}
func (f *failWorkWrite) Delete(ctx context.Context, p string) error { return f.Store.Delete(ctx, p) }
func (f *failWorkWrite) List(ctx context.Context, p string) iter.Seq2[blob.Entry, error] {
	return f.Store.List(ctx, p)
}

// retired reports whether the loser's grain carries the mergedInto marker.
func retired(t *testing.T, st blob.Store, loserID, loserURI string) bool {
	t.Helper()
	grain, _, err := st.Get(context.Background(), bibframe.AuthorityGrainPath(loserID))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(grain), "mergedInto")
}

// pointsAtLoser reports whether a Work grain still names the retired heading.
func pointsAtLoser(t *testing.T, st blob.Store, workID, loserURI string) bool {
	t.Helper()
	grain, _, err := st.Get(context.Background(), bibframe.GrainPath(workID))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(grain), loserURI)
}

func mergeAudits(t *testing.T, queue *suggest.Service) []suggest.AuditEntry {
	t.Helper()
	// Audit partitions by month; entries land under the writing service's clock.
	all, err := queue.Audit(context.Background(), time.Now().UTC().Format("2006-01"))
	if err != nil {
		t.Fatal(err)
	}
	var out []suggest.AuditEntry
	for _, e := range all {
		if e.Action == "AUTHORITY_MERGE" {
			out = append(out, e)
		}
	}
	return out
}

// twoCarriers seeds a loser heading carried by two Works, and returns the
// service, store, queue, loser id and URI.
func twoCarriers(t *testing.T) (*authoritiesvc.Service, blob.Store, *suggest.Service, string, string) {
	t.Helper()
	svc, st, queue, _ := newService(t)
	loserID, _, err := svc.Create(t.Context(), bibframe.AuthorityTerm{
		PrefLabel: map[string]string{"en": "Trans folks"},
	}, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	loserURI := bibframe.LocalAuthorityIRI(loserID)
	sub := &bibframe.AuthoritySubject{URI: loserURI, Labels: map[string]string{"en": "Trans folks"}}
	seedWork(t, st, "wcarrier00001", nil, sub, authoritiesvc.LocalScheme)
	seedWork(t, st, "wcarrier00002", nil, sub, authoritiesvc.LocalScheme)
	return svc, st, queue, loserID, loserURI
}

func winner() vocab.TermRef {
	return vocab.TermRef{Scheme: "homosaurus", ID: homoTransPeople}
}

// The retirement is a durable claim that the works were repointed. A rewrite that
// fails partway must not leave that claim behind.
func TestFailedMergeLeavesTheHeadingLive(t *testing.T) {
	svc, st, _, loserID, loserURI := twoCarriers(t)
	flaky := &failWorkWrite{Store: st, after: 1}
	svc.Blob = flaky

	result, err := svc.Merge(t.Context(), loserID, winner(), "lib@example.org")
	if !errors.Is(err, publish.ErrGrainStore) {
		t.Fatalf("err = %v, want a store failure wrapping publish.ErrGrainStore", err)
	}
	if result.Rewritten != 1 {
		t.Fatalf("rewrote %d works before failing, want 1", result.Rewritten)
	}
	// Exactly one landed: both would mean the fault never fired, none would mean
	// the count is a lie in the other direction.
	one, two := pointsAtLoser(t, st, "wcarrier00001", loserURI), pointsAtLoser(t, st, "wcarrier00002", loserURI)
	if one == two {
		t.Fatalf("expected exactly one work rewritten; still-pointing: w1=%v w2=%v", one, two)
	}
	if retired(t, st, loserID, loserURI) {
		t.Error("the loser was retired though the rewrite failed -- the catalog now describes one concept two ways")
	}
	// The control: the index agrees with the disk.
	if term, ok := svc.Vocab.Lookup(authoritiesvc.LocalScheme, loserURI); ok && term.MergedInto != "" {
		t.Errorf("the in-memory index reports mergedInto=%q after a failed merge", term.MergedInto)
	}
}

// A healthy merge still retires. Without this, the check above is satisfied by a
// Merge that never marks anything.
func TestSuccessfulMergeRetiresTheLoser(t *testing.T) {
	svc, st, _, loserID, loserURI := twoCarriers(t)
	result, err := svc.Merge(t.Context(), loserID, winner(), "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if result.Rewritten != 2 {
		t.Fatalf("rewrote %d works, want 2", result.Rewritten)
	}
	if !retired(t, st, loserID, loserURI) {
		t.Fatal("a clean merge did not retire the loser")
	}
	term, ok := svc.Vocab.Lookup(authoritiesvc.LocalScheme, loserURI)
	if !ok || term.MergedInto != homoTransPeople {
		t.Fatalf("index after a clean merge = %+v", term)
	}
	for _, id := range []string{"wcarrier00001", "wcarrier00002"} {
		if pointsAtLoser(t, st, id, loserURI) {
			t.Errorf("%s still names the retired heading", id)
		}
	}
}

// The audit trail is the only record that a heading was retired and N works
// repointed. It was written on the success path only -- absent for exactly the
// runs where somebody needs it.
func TestFailedMergeIsAudited(t *testing.T) {
	svc, st, queue, loserID, _ := twoCarriers(t)
	flaky := &failWorkWrite{Store: st, after: 1}
	svc.Blob = flaky

	if _, err := svc.Merge(t.Context(), loserID, winner(), "lib@example.org"); err == nil {
		t.Fatal("the seeded failure did not fire")
	}
	entries := mergeAudits(t, queue)
	if len(entries) != 1 {
		t.Fatalf("AUTHORITY_MERGE entries after a failed merge = %d, want 1", len(entries))
	}
	note := entries[0].Note
	if !strings.Contains(note, "1 of 2") {
		t.Errorf("the audit note does not carry the partial count: %q", note)
	}
	if !strings.Contains(note, "partial") || !strings.Contains(note, "retry") {
		t.Errorf("the audit note does not say it did not finish, nor what to do: %q", note)
	}

	// The control: the retry audits too, and says it finished. Without this, an
	// implementation that writes one entry and never another passes above.
	flaky.off = true
	if _, err := svc.Merge(t.Context(), loserID, winner(), "lib@example.org"); err != nil {
		t.Fatal(err)
	}
	entries = mergeAudits(t, queue)
	if len(entries) != 2 {
		t.Fatalf("AUTHORITY_MERGE entries after the resume = %d, want 2", len(entries))
	}
	for _, e := range entries {
		if strings.Contains(e.Note, "partial") && strings.Contains(e.Note, "1 of 1") {
			t.Errorf("the successful resume was audited as partial: %q", e.Note)
		}
	}
}

// The works a failed pass DID rewrite are real changes to real grains, and the
// downstream rebuild has to hear about them. Notify sat past the failure return
// with everything else, so a half-rewritten corpus never triggered a reprojection
// and the built site kept the old subjects until something unrelated moved.
func TestFailedMergeNotifiesTheGrainsItRewrote(t *testing.T) {
	svc, st, _, loserID, _ := twoCarriers(t)
	notifier := &fakeNotifier{}
	svc.Trigger = notifier
	flaky := &failWorkWrite{Store: st, after: 1}
	svc.Blob = flaky

	if _, err := svc.Merge(t.Context(), loserID, winner(), "x"); err == nil {
		t.Fatal("the seeded failure did not fire")
	}
	if len(notifier.events) != 1 {
		t.Fatalf("trigger events after a failed merge = %d, want 1", len(notifier.events))
	}
	// Exactly the one rewritten work grain -- not the loser, which was not touched.
	paths := notifier.events[0].Paths
	if len(paths) != 1 || !strings.HasPrefix(paths[0], "data/works/") {
		t.Errorf("notified paths = %v, want the single rewritten work grain", paths)
	}
}

// Nothing rewritten means nothing to notify: a merge that fails on its very first
// write must not trigger a rebuild of a corpus it never touched.
func TestMergeThatChangesNothingNotifiesNothing(t *testing.T) {
	svc, st, _, loserID, _ := twoCarriers(t)
	notifier := &fakeNotifier{}
	svc.Trigger = notifier
	flaky := &failWorkWrite{Store: st, after: 0}
	svc.Blob = flaky

	result, err := svc.Merge(t.Context(), loserID, winner(), "x")
	if err == nil {
		t.Fatal("the seeded failure did not fire")
	}
	if result.Rewritten != 0 {
		t.Fatalf("rewrote %d works, want 0", result.Rewritten)
	}
	if len(notifier.events) != 0 {
		t.Errorf("notified %v after changing nothing", notifier.events)
	}
}

// Retrying the identical request after the store recovers finishes the job. This
// held before the fix too -- AddAuthorityMergeMarker is idempotent and the loop
// skips works that no longer name the loser -- and it must keep holding, because
// "retry to finish" is what the 500 now tells the operator to do.
func TestMergeIsResumableAfterTheStoreRecovers(t *testing.T) {
	svc, st, _, loserID, loserURI := twoCarriers(t)
	flaky := &failWorkWrite{Store: st, after: 1}
	svc.Blob = flaky

	if _, err := svc.Merge(t.Context(), loserID, winner(), "lib@example.org"); err == nil {
		t.Fatal("the seeded failure did not fire")
	}
	flaky.off = true // the shard comes back

	result, err := svc.Merge(t.Context(), loserID, winner(), "lib@example.org")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	// One work was already repointed, so the retry rewrites exactly the other.
	if result.Rewritten != 1 {
		t.Fatalf("retry rewrote %d works, want 1 (the one that failed)", result.Rewritten)
	}
	for _, id := range []string{"wcarrier00001", "wcarrier00002"} {
		if pointsAtLoser(t, st, id, loserURI) {
			t.Errorf("%s still names the retired heading after the resume", id)
		}
	}
	if !retired(t, st, loserID, loserURI) {
		t.Error("the completed merge did not retire the loser")
	}
	// Exactly one marker: AddAuthorityMergeMarker must stay idempotent.
	grain, _, err := st.Get(t.Context(), bibframe.AuthorityGrainPath(loserID))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(grain), "mergedInto"); n != 1 {
		t.Errorf("loser grain carries %d mergedInto quads, want 1", n)
	}
}

// The in-memory index must never contradict the grain it was built from. Before
// the fix a failed merge retired the loser on disk while `Reload` sat past the
// failure return, so the running process still offered the heading and two
// operators on differently-aged processes disagreed about whether it existed.
//
// Note what actually fixes this: the **reorder**, not the reload. With the marker
// written last, a failed merge leaves no authority grain changed, so there is
// nothing for the index to be stale about. Merge still reloads on both paths as
// defence in depth, but this test cannot observe that -- stubbing the reload back
// onto the success path only leaves it green. Its job is the invariant, which is
// the thing an operator can see.
func TestIndexAgreesWithTheStoreAfterEitherOutcome(t *testing.T) {
	svc, st, _, loserID, loserURI := twoCarriers(t)
	flaky := &failWorkWrite{Store: st, after: 1}
	svc.Blob = flaky
	if _, err := svc.Merge(t.Context(), loserID, winner(), "x"); err == nil {
		t.Fatal("the seeded failure did not fire")
	}
	onDisk := retired(t, st, loserID, loserURI)
	term, _ := svc.Vocab.Lookup(authoritiesvc.LocalScheme, loserURI)
	inIndex := term.MergedInto != ""
	if onDisk != inIndex {
		t.Fatalf("after a failed merge: grain retired=%v, index mergedInto=%v", onDisk, inIndex)
	}

	flaky.off = true
	if _, err := svc.Merge(t.Context(), loserID, winner(), "x"); err != nil {
		t.Fatal(err)
	}
	onDisk = retired(t, st, loserID, loserURI)
	term, _ = svc.Vocab.Lookup(authoritiesvc.LocalScheme, loserURI)
	inIndex = term.MergedInto != ""
	if !onDisk || !inIndex {
		t.Fatalf("after a clean merge: grain retired=%v, index mergedInto=%v", onDisk, inIndex)
	}
}
