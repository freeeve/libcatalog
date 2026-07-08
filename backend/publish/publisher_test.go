package publish

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/trigger"
	"github.com/freeeve/libcat/backend/vocab"
)

const (
	workID   = "wabc123def456"
	transURI = "https://homosaurus.org/v4/homoit0001235"
)

// seedGrain writes a minimal feed grain for workID into the blob store.
func seedGrain(t *testing.T, bs blob.Store) string {
	t.Helper()
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	ds.Add(work, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), rdf.NewLiteral("A Book", "", ""), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	path := bibframe.GrainPath(workID)
	if _, err := bs.Put(t.Context(), path, nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureNotifier records events.
type captureNotifier struct {
	mu     sync.Mutex
	events []trigger.Event
}

func (c *captureNotifier) Notify(ctx context.Context, e trigger.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}

func newPublisher(t *testing.T) (*Publisher, blob.Store, *suggest.Service, *captureNotifier) {
	t.Helper()
	data, err := os.ReadFile("../vocab/testdata/authorities.nq")
	if err != nil {
		t.Fatal(err)
	}
	authorities := blob.NewMem()
	_, _ = authorities.Put(t.Context(), "a/x.nq", data, blob.PutOptions{})
	ix, err := vocab.Load(t.Context(), authorities, "a/", nil)
	if err != nil {
		t.Fatal(err)
	}
	db := store.NewMem()
	queue := suggest.New(db, ix, suggest.Caps{})
	grains := blob.NewMem()
	notifier := &captureNotifier{}
	pub := &Publisher{
		Blob: grains, Queue: queue, Vocab: ix,
		Trigger: notifier, Lease: NewLease(db, "ingest", time.Minute),
	}
	return pub, grains, queue, notifier
}

func TestPublishApproved(t *testing.T) {
	pub, grains, queue, notifier := newPublisher(t)
	path := seedGrain(t, grains)

	// Approve one controlled subject and one folk tag for the work.
	if err := queue.ManualTerm(t.Context(), workID, vocab.TermRef{Scheme: "homosaurus", ID: transURI}, "A Book", "lib"); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Submit(t.Context(), suggest.SubmitInput{
		WorkID: workID, Term: vocab.TermRef{Scheme: vocab.FolkScheme, ID: "cozy fantasy"},
		Type: suggest.TypeAdd, SupporterHash: "h1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := queue.Review(t.Context(), []suggest.Decision{{
		WorkID: workID, Term: vocab.TermRef{Scheme: vocab.FolkScheme, ID: "cozy fantasy"},
		Type: suggest.TypeAdd, Approve: true,
	}}, "lib"); err != nil {
		t.Fatal(err)
	}

	result, err := pub.PublishApproved(t.Context(), "lib")
	if err != nil {
		t.Fatalf("PublishApproved: %v", err)
	}
	if result.Published != 2 || result.Skipped != 0 || len(result.Paths) != 1 {
		t.Fatalf("result = %+v", result)
	}

	grain, _, err := grains.Get(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(grain)
	for _, want := range []string{
		"<editorial:>", transURI, // controlled subject link
		"<authority:homosaurus>", "Transgender people", // labels ride along
		"cozy fantasy", bibframe.PredTag, // folk tag
		"A Book", // feed untouched
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("grain missing %q:\n%s", want, text)
		}
	}
	// Worklist drained, etag stamped, audit written, trigger fired.
	pending, _ := queue.ApprovedUnpublished(t.Context())
	if len(pending) != 0 {
		t.Fatalf("worklist not drained: %+v", pending)
	}
	items, _ := queue.ForWork(t.Context(), workID)
	for _, sg := range items {
		if sg.PublishedETag == "" {
			t.Fatalf("unstamped item %+v", sg)
		}
	}
	if len(notifier.events) != 1 || notifier.events[0].Paths[0] != path {
		t.Fatalf("trigger events = %+v", notifier.events)
	}
	month := time.Now().UTC().Format("2006-01")
	audit, _ := queue.Audit(t.Context(), month)
	var publishAudits int
	for _, e := range audit {
		if e.Action == "PUBLISH_DONE" && e.ETag != "" {
			publishAudits++
		}
	}
	if publishAudits != 1 {
		t.Fatalf("audit = %+v", audit)
	}

	// Re-publishing with an empty worklist is a clean no-op.
	again, err := pub.PublishApproved(t.Context(), "lib")
	if err != nil || again.Published != 0 {
		t.Fatalf("re-publish = %+v, %v", again, err)
	}
}

func TestPublishDefersDuringIngest(t *testing.T) {
	pub, grains, queue, _ := newPublisher(t)
	seedGrain(t, grains)
	if err := queue.ManualTerm(t.Context(), workID, vocab.TermRef{Scheme: "homosaurus", ID: transURI}, "", "lib"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := pub.Lease.Acquire(t.Context(), "ingest-run"); !ok {
		t.Fatal("lease acquire failed")
	}
	if _, err := pub.PublishApproved(t.Context(), "lib"); !errors.Is(err, ErrIngestActive) {
		t.Fatalf("publish during ingest: %v", err)
	}
	// Approval stayed queued.
	pending, _ := queue.ApprovedUnpublished(t.Context())
	if len(pending) != 1 {
		t.Fatalf("worklist = %+v", pending)
	}
	// Lease released -> publish proceeds.
	if err := pub.Lease.Release(t.Context(), "ingest-run"); err != nil {
		t.Fatal(err)
	}
	if result, err := pub.PublishApproved(t.Context(), "lib"); err != nil || result.Published != 1 {
		t.Fatalf("after release = %+v, %v", result, err)
	}
}

func TestPublishSkipsMissingGrain(t *testing.T) {
	pub, _, queue, _ := newPublisher(t)
	// No grain seeded.
	if err := queue.ManualTerm(t.Context(), workID, vocab.TermRef{Scheme: "homosaurus", ID: transURI}, "", "lib"); err != nil {
		t.Fatal(err)
	}
	result, err := pub.PublishApproved(t.Context(), "lib")
	if err != nil || result.Skipped != 1 || result.Published != 0 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	// Item stays queued for operator attention.
	pending, _ := queue.ApprovedUnpublished(t.Context())
	if len(pending) != 1 {
		t.Fatalf("worklist = %+v", pending)
	}
}

// TestEditorReingestRace is the §5 never-lose-editorial invariant under
// concurrency: an editorial publish and a feed re-ingest (rebuild feed graph,
// preserve non-feed graphs) race on the same grain through conditional
// writes; both changes must survive.
func TestEditorReingestRace(t *testing.T) {
	for round := range 25 {
		_ = round
		grains := blob.NewMem()
		path := seedGrain(t, grains)

		editorDone := make(chan error, 1)
		ingestDone := make(chan error, 1)

		// Editor: add an editorial subject via CAS.
		go func() {
			_, err := MutateGrain(context.Background(), grains, path, func(old []byte) ([]byte, error) {
				return bibframe.ApplyEditorialPatch(old, bibframe.Patch{Add: []rdf.Quad{
					bibframe.SubjectQuad(workID, transURI),
				}})
			})
			editorDone <- err
		}()

		// Re-ingest: rewrite the feed graph (new title statement), carrying
		// every non-feed graph, via the same CAS discipline.
		go func() {
			_, err := MutateGrain(context.Background(), grains, path, func(old []byte) ([]byte, error) {
				ds, err := rdf.ParseNQuads(old)
				if err != nil {
					return nil, err
				}
				feed := bibframe.FeedGraph("overdrive")
				keep := ds.Quads[:0]
				for _, q := range ds.Quads {
					if q.G != feed {
						keep = append(keep, q)
					}
				}
				ds.Quads = keep
				work := rdf.NewIRI(bibframe.WorkIRI(workID))
				ds.Add(work, rdf.NewIRI("http://id.loc.gov/ontologies/bibframe/title"), rdf.NewLiteral("A Book, Revised", "", ""), feed)
				return ds.Canonical()
			})
			ingestDone <- err
		}()

		if err := <-editorDone; err != nil {
			t.Fatalf("editor: %v", err)
		}
		if err := <-ingestDone; err != nil {
			t.Fatalf("ingest: %v", err)
		}
		final, _, err := grains.Get(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(final)
		if !strings.Contains(text, transURI) {
			t.Fatalf("editorial statement lost:\n%s", text)
		}
		if !strings.Contains(text, "A Book, Revised") {
			t.Fatalf("feed rewrite lost:\n%s", text)
		}
	}
}
