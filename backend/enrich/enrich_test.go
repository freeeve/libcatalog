package enrich

import (
	"context"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
)

const bfNS = "http://id.loc.gov/ontologies/bibframe/"

func fixtureStore(t *testing.T) blob.Store {
	t.Helper()
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI("wenrich000001"))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	title := rdf.NewBlank("t0")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), title, feed)
	ds.Add(title, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral("A Book", "", ""), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	st := blob.NewMem()
	if _, err := st.Put(t.Context(), bibframe.GrainPath("wenrich000001"), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	return st
}

type stubEnricher struct{}

func (stubEnricher) Name() string { return "stub" }
func (stubEnricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	var out []ingest.Enrichment
	for _, w := range works {
		out = append(out, ingest.Enrichment{
			WorkID:     w.WorkID,
			Confidence: 0.95,
			Subjects: []bibframe.AuthoritySubject{{
				URI:    "http://id.loc.gov/authorities/subjects/sh85118553",
				Labels: map[string]string{"en": "Science fiction"},
			}},
		})
	}
	return out, nil
}

func TestQueueMode(t *testing.T) {
	bs := fixtureStore(t)
	queue := suggest.New(store.NewMem(), nil, suggest.Caps{})
	svc := &Service{
		Blob: bs, Queue: queue,
		Sources: map[string]Source{"stub": {Enricher: stubEnricher{}, Mode: ModeQueue, Scheme: "lcsh"}},
	}
	result, err := svc.Run(t.Context(), "stub")
	if err != nil || result.Works != 1 || result.Mode != ModeQueue {
		t.Fatalf("result = %+v, %v", result, err)
	}
	// A PIPELINE suggestion is in the moderation queue with confidence.
	page, err := queue.Queue(t.Context(), suggest.QueueQuery{Provenance: suggest.ProvenancePipeline})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("queue = %+v, %v", page, err)
	}
	item := page.Items[0]
	if item.Term.Scheme != "lcsh" || item.Confidence != 0.95 || item.Term.Label != "Science fiction" {
		t.Fatalf("item = %+v", item)
	}
	// Re-running never duplicates or resets moderation state.
	if _, err := svc.Run(t.Context(), "stub"); err != nil {
		t.Fatal(err)
	}
	page, _ = queue.Queue(t.Context(), suggest.QueueQuery{})
	if len(page.Items) != 1 {
		t.Fatalf("re-run duplicated: %+v", page.Items)
	}
	// A rejected+tombstoned pair never comes back.
	if err := queue.Review(t.Context(), []suggest.Decision{{
		WorkID: item.WorkID, Term: item.Term, Type: suggest.TypeAdd, Tombstone: true,
	}}, "lib"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Run(t.Context(), "stub"); err != nil {
		t.Fatal(err)
	}
	page, _ = queue.Queue(t.Context(), suggest.QueueQuery{})
	if len(page.Items) != 0 {
		t.Fatalf("tombstoned pair re-queued: %+v", page.Items)
	}
	// The grain was never touched in queue mode.
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath("wenrich000001"))
	if strings.Contains(string(grain), "sh85118553") {
		t.Fatal("queue mode wrote the grain")
	}
}

func TestDirectMode(t *testing.T) {
	bs := fixtureStore(t)
	svc := &Service{
		Blob:    bs,
		Sources: map[string]Source{"stub": {Enricher: stubEnricher{}, Mode: ModeDirect}},
	}
	result, err := svc.Run(t.Context(), "stub")
	if err != nil || result.Works != 1 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	grain, _, _ := bs.Get(t.Context(), bibframe.GrainPath("wenrich000001"))
	if !strings.Contains(string(grain), "<enrichment:stub>") || !strings.Contains(string(grain), "sh85118553") {
		t.Fatalf("direct mode did not write:\n%s", grain)
	}
	if _, err := svc.Run(t.Context(), "nope"); err == nil {
		t.Fatal("unknown source accepted")
	}
}
