package authoritiesvc_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/authoritiesvc"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/trigger"
	"github.com/freeeve/libcat/backend/vocab"
)

const homosaurusFixture = `<https://homosaurus.org/v4/homoit0001235> <http://www.w3.org/2004/02/skos/core#prefLabel> "Transgender people"@en <authority:homosaurus> .
<https://homosaurus.org/v4/homoit0001235> <http://www.w3.org/2004/02/skos/core#altLabel> "Trans people"@en <authority:homosaurus> .
<https://homosaurus.org/v4/homoit0001235> <http://www.w3.org/2004/02/skos/core#broader> <https://homosaurus.org/v4/homoit0000508> <authority:homosaurus> .
<https://homosaurus.org/v4/homoit0000508> <http://www.w3.org/2004/02/skos/core#prefLabel> "Gender identity"@en <authority:homosaurus> .
`

const homoTransPeople = "https://homosaurus.org/v4/homoit0001235"

type fakeNotifier struct{ events []trigger.Event }

func (f *fakeNotifier) Notify(_ context.Context, e trigger.Event) error {
	f.events = append(f.events, e)
	return nil
}

// newService assembles the service over in-memory stores with the homosaurus
// fixture pre-seeded.
func newService(t *testing.T) (*authoritiesvc.Service, blob.Store, *suggest.Service, *fakeNotifier) {
	t.Helper()
	st := blob.NewMem()
	if _, err := st.Put(t.Context(), "data/authorities/ho/homosaurus.nq", []byte(homosaurusFixture), blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	ix, err := vocab.Load(t.Context(), st, "data/authorities/", nil)
	if err != nil {
		t.Fatal(err)
	}
	queue := suggest.New(store.NewMem(), ix, suggest.Caps{})
	notifier := &fakeNotifier{}
	svc := &authoritiesvc.Service{Blob: st, Vocab: ix, Queue: queue, Trigger: notifier}
	return svc, st, queue, notifier
}

// seedWork writes a Work grain with a feed title, feed blank-node tags, and
// optionally an editorial controlled-subject reference.
func seedWork(t *testing.T, st blob.Store, workID string, tags []string, subject *bibframe.AuthoritySubject, subjectScheme string) {
	t.Helper()
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI(workID))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	title := rdf.NewBlank("t0")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), title, feed)
	ds.Add(title, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral("A Book", "", ""), feed)
	for i, tag := range tags {
		node := rdf.NewBlank("s" + string(rune('0'+i)))
		ds.Add(work, rdf.NewIRI(bfNS+"subject"), node, feed)
		ds.Add(node, rdf.NewIRI("http://www.w3.org/2000/01/rdf-schema#label"), rdf.NewLiteral(tag, "", ""), feed)
	}
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if subject != nil {
		nq, err = bibframe.AppendAuthoritySubject(nq, workID, *subject, subjectScheme)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Put(t.Context(), bibframe.GrainPath(workID), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestCreateGetUpdate(t *testing.T) {
	svc, _, _, _ := newService(t)
	term := bibframe.AuthorityTerm{
		PrefLabel: map[string]string{"en": "Cozy fantasy"},
		AltLabel:  map[string][]string{"en": {"Comfort fantasy"}},
	}
	id, etag, err := svc.Create(t.Context(), term, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if !authoritiesvc.IDPattern.MatchString(id) {
		t.Fatalf("id = %q", id)
	}
	uri := bibframe.LocalAuthorityIRI(id)

	// Create round-trips through the grain and lands in the live index.
	got, gotTag, err := svc.Get(t.Context(), id)
	if err != nil || gotTag != etag {
		t.Fatalf("get: %v (etag %q vs %q)", err, gotTag, etag)
	}
	if got.URI != uri || got.PrefLabel["en"] != "Cozy fantasy" {
		t.Fatalf("doc = %+v", got)
	}
	if _, ok := svc.Vocab.Lookup(authoritiesvc.LocalScheme, uri); !ok {
		t.Fatal("created term not in index")
	}
	if hits := svc.Vocab.Search(authoritiesvc.LocalScheme, "cozy", 5); len(hits) != 1 {
		t.Fatalf("search after create = %v", hits)
	}

	// Update relabels globally: the index serves the new label on reload.
	got.PrefLabel["en"] = "Cozy fantasy (genre)"
	newTag, err := svc.Update(t.Context(), id, got, etag, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if term, _ := svc.Vocab.Lookup(authoritiesvc.LocalScheme, uri); term.Labels["en"] != "Cozy fantasy (genre)" {
		t.Fatalf("index label after update = %+v", term)
	}

	// Stale token loses.
	if _, err := svc.Update(t.Context(), id, got, etag, "lib@example.org"); !errors.Is(err, blob.ErrPreconditionFailed) {
		t.Fatalf("stale update err = %v", err)
	}
	_ = newTag

	// Validation floor: no preferred label, no save.
	if _, _, err := svc.Create(t.Context(), bibframe.AuthorityTerm{}, "x"); !errors.Is(err, authoritiesvc.ErrValidation) {
		t.Fatalf("create empty err = %v", err)
	}
	if _, _, err := svc.Get(t.Context(), "amissing000001"); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("get missing err = %v", err)
	}
}

func TestMergeRewritesCorpus(t *testing.T) {
	svc, st, _, notifier := newService(t)
	loserID, _, err := svc.Create(t.Context(), bibframe.AuthorityTerm{
		PrefLabel: map[string]string{"en": "Trans folks"},
	}, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	loserURI := bibframe.LocalAuthorityIRI(loserID)
	seedWork(t, st, "wcarrier00001", nil, &bibframe.AuthoritySubject{
		URI: loserURI, Labels: map[string]string{"en": "Trans folks"},
	}, authoritiesvc.LocalScheme)
	seedWork(t, st, "wbystander001", nil, nil, "")

	result, err := svc.Merge(t.Context(), loserID, vocab.TermRef{
		Scheme: "homosaurus", ID: homoTransPeople,
	}, "lib@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if result.Rewritten != 1 || result.Loser != loserURI || result.Winner != homoTransPeople {
		t.Fatalf("result = %+v", result)
	}

	// The carrier now references the winner (with its authority labels);
	// no trace of the loser remains in it.
	grain, _, err := st.Get(t.Context(), bibframe.GrainPath("wcarrier00001"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(grain), loserURI) {
		t.Fatalf("loser survives in carrier:\n%s", grain)
	}
	if !strings.Contains(string(grain), homoTransPeople) || !strings.Contains(string(grain), "Transgender people") {
		t.Fatalf("winner missing in carrier:\n%s", grain)
	}

	// The loser grain records the retirement, and the live index reflects
	// it: resolvable, but out of search.
	term, ok := svc.Vocab.Lookup(authoritiesvc.LocalScheme, loserURI)
	if !ok || term.MergedInto != homoTransPeople {
		t.Fatalf("index after merge = %+v", term)
	}
	if hits := svc.Vocab.Search(authoritiesvc.LocalScheme, "trans folks", 5); hits != nil {
		t.Fatalf("retired term searchable = %v", hits)
	}

	// Downstream rebuild notified with the loser + carrier paths.
	if len(notifier.events) != 1 || len(notifier.events[0].Paths) != 2 {
		t.Fatalf("events = %+v", notifier.events)
	}

	// Merging into itself or a missing loser fails closed.
	if _, err := svc.Merge(t.Context(), loserID, vocab.TermRef{Scheme: authoritiesvc.LocalScheme, ID: loserURI}, "x"); !errors.Is(err, authoritiesvc.ErrValidation) {
		t.Fatalf("self-merge err = %v", err)
	}
	if _, err := svc.Merge(t.Context(), "amissing000001", vocab.TermRef{Scheme: "homosaurus", ID: homoTransPeople}, "x"); !errors.Is(err, blob.ErrNotFound) {
		t.Fatalf("missing loser err = %v", err)
	}
}

func TestAutoLink(t *testing.T) {
	svc, st, queue, _ := newService(t)
	// One tag matches a pref label whole, one is unknown; matching is
	// whole-heading only, so "Trans" (a prefix) must not link.
	seedWork(t, st, "wautolink0001", []string{"Transgender People", "Space Opera", "Trans"}, nil, "")
	grain, _, err := st.Get(t.Context(), bibframe.GrainPath("wautolink0001"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := svc.AutoLink(t.Context(), "wautolink0001", grain)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("enqueued = %d", n)
	}
	items, err := queue.ForWork(t.Context(), "wautolink0001")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queue = %+v", items)
	}
	sg := items[0]
	if sg.Provenance != suggest.ProvenancePipeline || sg.Status != suggest.StatusPending {
		t.Fatalf("suggestion = %+v", sg)
	}
	if sg.Term.Scheme != "homosaurus" || sg.Term.ID != homoTransPeople {
		t.Fatalf("term = %+v", sg.Term)
	}
	if sg.Confidence != 0.9 {
		t.Fatalf("confidence = %v", sg.Confidence)
	}

	// Re-running never spams the queue (create-only aggregates).
	if _, err := svc.AutoLink(t.Context(), "wautolink0001", grain); err != nil {
		t.Fatal(err)
	}
	if items, _ := queue.ForWork(t.Context(), "wautolink0001"); len(items) != 1 {
		t.Fatalf("queue after rerun = %+v", items)
	}

	// A Work already carrying the controlled subject gets no suggestion.
	seedWork(t, st, "wautolink0002", []string{"Transgender People"}, &bibframe.AuthoritySubject{
		URI: homoTransPeople, Labels: map[string]string{"en": "Transgender people"},
	}, "homosaurus")
	grain2, _, err := st.Get(t.Context(), bibframe.GrainPath("wautolink0002"))
	if err != nil {
		t.Fatal(err)
	}
	if n, err := svc.AutoLink(t.Context(), "wautolink0002", grain2); err != nil || n != 0 {
		t.Fatalf("covered tag enqueued %d, %v", n, err)
	}
}
