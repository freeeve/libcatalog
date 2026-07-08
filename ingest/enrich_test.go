package ingest_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"
)

// enrichFixture builds one grain with a Work carrying a title, contributor,
// blank-node feed tag, editorial lcat:tag, and an Instance with an ISBN.
func enrichFixture(t *testing.T) blob.Store {
	t.Helper()
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	work := rdf.NewIRI(bibframe.WorkIRI("wenrich000001"))
	ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
	title := rdf.NewBlank("t0")
	ds.Add(work, rdf.NewIRI(bfNS+"title"), title, feed)
	ds.Add(title, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral("Gideon the Ninth", "", ""), feed)
	contrib := rdf.NewBlank("c0")
	agent := rdf.NewBlank("a0")
	ds.Add(work, rdf.NewIRI(bfNS+"contribution"), contrib, feed)
	ds.Add(contrib, rdf.NewIRI(bfNS+"agent"), agent, feed)
	ds.Add(agent, rdf.NewIRI("http://www.w3.org/2000/01/rdf-schema#label"), rdf.NewLiteral("Muir, Tamsyn", "", ""), feed)
	tag := rdf.NewBlank("s0")
	ds.Add(work, rdf.NewIRI(bfNS+"subject"), tag, feed)
	ds.Add(tag, rdf.NewIRI("http://www.w3.org/2000/01/rdf-schema#label"), rdf.NewLiteral("Science Fiction", "", ""), feed)
	inst := rdf.NewIRI(bibframe.InstanceIRI("ienrich000001"))
	ds.Add(work, rdf.NewIRI(bfNS+"hasInstance"), inst, feed)
	isbn := rdf.NewBlank("i0")
	ds.Add(inst, rdf.NewIRI(bfNS+"identifiedBy"), isbn, feed)
	ds.Add(isbn, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Isbn"), feed)
	ds.Add(isbn, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#value"), rdf.NewLiteral("9781250313195", "", ""), feed)
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	nq, err = bibframe.ApplyEditorialPatch(nq, bibframe.Patch{Add: []rdf.Quad{
		bibframe.TagQuad("wenrich000001", "necromancy"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	st := blob.NewMem()
	if _, err := st.Put(t.Context(), bibframe.GrainPath("wenrich000001"), nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestScanSummaries(t *testing.T) {
	st := enrichFixture(t)
	summaries, paths, err := ingest.ScanSummaries(t.Context(), st, "data/works/")
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %+v", summaries)
	}
	s := summaries[0]
	if s.WorkID != "wenrich000001" || s.Title != "Gideon the Ninth" {
		t.Fatalf("summary = %+v", s)
	}
	if len(s.Contributors) != 1 || s.Contributors[0] != "Muir, Tamsyn" {
		t.Fatalf("contributors = %v", s.Contributors)
	}
	// Both the feed blank-node tag and the editorial lcat:tag surface.
	if len(s.Tags) != 2 || s.Tags[0] != "Science Fiction" && s.Tags[1] != "Science Fiction" {
		t.Fatalf("tags = %v", s.Tags)
	}
	if len(s.ISBNs) != 1 || s.ISBNs[0] != "9781250313195" {
		t.Fatalf("isbns = %v", s.ISBNs)
	}
	if len(s.Subjects) != 0 {
		t.Fatalf("blank-node-only fixture should carry no controlled subjects: %v", s.Subjects)
	}
	if paths[s.WorkID] == "" {
		t.Fatalf("paths = %v", paths)
	}
}

// TestSummarizeGrainControlledSubjects covers the IRI-object bf:subject
// dimension merges rewrite (tasks/046): controlled references surface in
// Subjects, uncontrolled blank-node labels stay in Tags.
func TestSummarizeGrainControlledSubjects(t *testing.T) {
	st := enrichFixture(t)
	grain, _, err := st.Get(t.Context(), bibframe.GrainPath("wenrich000001"))
	if err != nil {
		t.Fatal(err)
	}
	const termURI = "https://homosaurus.org/v4/homoit0001235"
	grain, err = bibframe.AppendAuthoritySubject(grain, "wenrich000001", bibframe.AuthoritySubject{
		URI: termURI, Labels: map[string]string{"en": "Transgender people"},
	}, "homosaurus")
	if err != nil {
		t.Fatal(err)
	}
	summaries, err := ingest.SummarizeGrain(grain)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %+v", summaries)
	}
	s := summaries[0]
	if len(s.Subjects) != 1 || s.Subjects[0] != termURI {
		t.Fatalf("subjects = %v", s.Subjects)
	}
	if len(s.Tags) != 2 {
		t.Fatalf("tags disturbed = %v", s.Tags)
	}
}

// fakeEnricher asserts one subject on every Work it sees.
type fakeEnricher struct {
	name    string
	subject bibframe.AuthoritySubject
	calls   int
}

func (f *fakeEnricher) Name() string { return f.name }
func (f *fakeEnricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	f.calls++
	var out []ingest.Enrichment
	for _, w := range works {
		e := ingest.Enrichment{WorkID: w.WorkID, Confidence: 1}
		if f.subject.URI != "" {
			e.Subjects = []bibframe.AuthoritySubject{f.subject}
		}
		out = append(out, e) // empty Subjects = explicit withdrawal
	}
	return out, nil
}

func TestRunEnrichDirect(t *testing.T) {
	st := enrichFixture(t)
	e := &fakeEnricher{name: "testsource", subject: bibframe.AuthoritySubject{
		URI:     "http://id.loc.gov/authorities/subjects/sh85118553",
		Labels:  map[string]string{"en": "Science fiction"},
		Broader: []string{"http://id.loc.gov/authorities/subjects/sh85045198"},
	}}
	n, err := ingest.RunEnrich(t.Context(), st, "data/works/", e)
	if err != nil || n != 1 {
		t.Fatalf("RunEnrich = %d, %v", n, err)
	}
	path := bibframe.GrainPath("wenrich000001")
	grain, _, _ := st.Get(t.Context(), path)
	text := string(grain)
	for _, want := range []string{"<enrichment:testsource>", "sh85118553", "Science fiction", "sh85045198"} {
		if !strings.Contains(text, want) {
			t.Fatalf("grain missing %q:\n%s", want, text)
		}
	}
	// Idempotent re-run: byte-identical.
	if _, err := ingest.RunEnrich(t.Context(), st, "data/works/", e); err != nil {
		t.Fatal(err)
	}
	again, _, _ := st.Get(t.Context(), path)
	if !bytes.Equal(grain, again) {
		t.Fatal("re-enrichment changed the grain")
	}
	// Feed re-ingest preserves the enrichment graph (preservedQuads keeps
	// every non-feed graph -- asserted end-to-end in bibframe tests; here we
	// assert the editorial tag also survived enrichment).
	if !strings.Contains(text, "necromancy") {
		t.Fatal("editorial tag lost during enrichment")
	}
	// Explicit withdrawal (an Enrichment with no Subjects) clears the
	// source's graph -- and only its graph.
	e.subject = bibframe.AuthoritySubject{}
	if _, err := ingest.RunEnrich(t.Context(), st, "data/works/", e); err != nil {
		t.Fatal(err)
	}
	cleared, _, _ := st.Get(t.Context(), path)
	if strings.Contains(string(cleared), "enrichment:testsource") {
		t.Fatalf("withdrawal left enrichment statements:\n%s", cleared)
	}
	if !strings.Contains(string(cleared), "necromancy") || !strings.Contains(string(cleared), "Gideon the Ninth") {
		t.Fatal("withdrawal touched other graphs")
	}
}

// mapEnricher enriches only the Works it has a subject for; others are
// absent from its results (the "left untouched" contract).
type mapEnricher struct {
	name   string
	byWork map[string]bibframe.AuthoritySubject
}

func (m *mapEnricher) Name() string { return m.name }
func (m *mapEnricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	var out []ingest.Enrichment
	for _, w := range works {
		if subj, ok := m.byWork[w.WorkID]; ok {
			out = append(out, ingest.Enrichment{WorkID: w.WorkID, Subjects: []bibframe.AuthoritySubject{subj}, Confidence: 1})
		}
	}
	return out, nil
}

// TestRunEnrichSharedGrain covers post-merge grains holding two Works
// (tasks/102): enriching both writes both, and a later run that returns only
// one Work must not wipe the other's statements from the shared graph.
func TestRunEnrichSharedGrain(t *testing.T) {
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	const workA, workB = "wshare0000a1", "wshare0000b2"
	ds := &rdf.Dataset{}
	feed := bibframe.FeedGraph("overdrive")
	for i, id := range []string{workA, workB} {
		work := rdf.NewIRI(bibframe.WorkIRI(id))
		ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), feed)
		title := rdf.NewBlank("t" + string(rune('0'+i)))
		ds.Add(work, rdf.NewIRI(bfNS+"title"), title, feed)
		ds.Add(title, rdf.NewIRI(bfNS+"mainTitle"), rdf.NewLiteral("Title "+id, "", ""), feed)
	}
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	st := blob.NewMem()
	path := bibframe.GrainPath(workA)
	if _, err := st.Put(t.Context(), path, nq, blob.PutOptions{}); err != nil {
		t.Fatal(err)
	}

	subjX := bibframe.AuthoritySubject{URI: "http://example.org/term/X", Labels: map[string]string{"en": "Term X"}}
	subjY := bibframe.AuthoritySubject{URI: "http://example.org/term/Y", Labels: map[string]string{"en": "Term Y"}}
	subjZ := bibframe.AuthoritySubject{URI: "http://example.org/term/Z", Labels: map[string]string{"en": "Term Z"}}

	// Both Works enriched in one run: both must land in the shared graph.
	both := &mapEnricher{name: "shared", byWork: map[string]bibframe.AuthoritySubject{workA: subjX, workB: subjY}}
	if n, err := ingest.RunEnrich(t.Context(), st, "data/works/", both); err != nil || n != 2 {
		t.Fatalf("RunEnrich = %d, %v", n, err)
	}
	grain, _, _ := st.Get(t.Context(), path)
	for _, want := range []string{"term/X", "Term X", "term/Y", "Term Y"} {
		if !strings.Contains(string(grain), want) {
			t.Fatalf("shared grain missing %q after enriching both Works:\n%s", want, grain)
		}
	}

	// A run returning only workA (fresh subject Z) must replace A's statements
	// and leave workB's untouched.
	onlyA := &mapEnricher{name: "shared", byWork: map[string]bibframe.AuthoritySubject{workA: subjZ}}
	if n, err := ingest.RunEnrich(t.Context(), st, "data/works/", onlyA); err != nil || n != 1 {
		t.Fatalf("RunEnrich = %d, %v", n, err)
	}
	grain, _, _ = st.Get(t.Context(), path)
	text := string(grain)
	for _, want := range []string{"term/Z", "Term Z", "term/Y", "Term Y"} {
		if !strings.Contains(text, want) {
			t.Fatalf("selective re-run lost %q (sibling Work wiped, tasks/102):\n%s", want, text)
		}
	}
	if strings.Contains(text, "term/X") {
		t.Fatalf("workA's stale term X should have been replaced:\n%s", text)
	}

	// Idempotent: repeating the selective run changes nothing.
	if _, err := ingest.RunEnrich(t.Context(), st, "data/works/", onlyA); err != nil {
		t.Fatal(err)
	}
	again, _, _ := st.Get(t.Context(), path)
	if !bytes.Equal(grain, again) {
		t.Fatal("selective re-enrichment changed the grain")
	}
}
