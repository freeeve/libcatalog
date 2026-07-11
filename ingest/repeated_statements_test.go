// libcodex v0.24.0 made rdf.Graph.Objects return each distinct object
// once. Graph is the document's triple list, not a set -- real serializations
// restate triples constantly, and SummarizeDataset additionally merges every
// named graph into one list, so a statement carried by both the feed graph and
// the editorial graph appeared twice.
//
// s.Items was a len() straight off that slice, so a Work with one item could
// report two. These tests pin the counts against a fixture that repeats every
// statement the summary reads.
package ingest_test

import (
	"testing"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"
)

const bfNS = "http://id.loc.gov/ontologies/bibframe/"

// restated builds a grain whose Work and Instance statements are each asserted
// in two graphs: the shape a feed re-ingest plus an editorial edit produces.
// Every statement is byte-identical, so nothing here is a genuine second value.
func restated(t *testing.T) []ingest.WorkSummary {
	t.Helper()
	ds := &rdf.Dataset{}
	work := rdf.NewIRI(bibframe.WorkIRI("wdup00000001"))
	inst := rdf.NewIRI(bibframe.InstanceIRI("idup00000001"))
	item := rdf.NewIRI("#idup00000001Item1")

	for _, gr := range []rdf.Term{bibframe.FeedGraph("overdrive"), bibframe.FeedGraph("loc")} {
		ds.Add(work, rdf.NewIRI("http://www.w3.org/1999/02/22-rdf-syntax-ns#type"), rdf.NewIRI(bfNS+"Work"), gr)
		ds.Add(work, rdf.NewIRI(bfNS+"hasInstance"), inst, gr)
		ds.Add(work, rdf.NewIRI(bfNS+"language"), rdf.NewIRI("http://id.loc.gov/vocabulary/languages/eng"), gr)
		ds.Add(work, rdf.NewIRI(bfNS+"subject"), rdf.NewIRI("http://id.loc.gov/authorities/subjects/sh85077507"), gr)
		ds.Add(work, rdf.NewIRI(bibframe.PredTag), rdf.NewLiteral("poetry", "", ""), gr)
		ds.Add(inst, rdf.NewIRI(bfNS+"hasItem"), item, gr)
		ds.Add(inst, rdf.NewIRI(bfNS+"seriesStatement"), rdf.NewLiteral("The Locked Tomb", "", ""), gr)
	}
	nq, err := ds.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	sums, err := ingest.SummarizeGrain(nq)
	if err != nil {
		t.Fatal(err)
	}
	if len(sums) != 1 {
		t.Fatalf("summaries = %d, want 1", len(sums))
	}
	return sums
}

// The bug the libcodex note names: one item, stated twice, counted twice.
func TestItemsCountsDistinctItems(t *testing.T) {
	if got := restated(t)[0].Items; got != 1 {
		t.Fatalf("Items = %d, want 1: a restated bf:hasItem is one holding, not two", got)
	}
}

// The same reasoning covers everything else the summary reads off Objects.
func TestRestatedStatementsDoNotDuplicateSummaryValues(t *testing.T) {
	s := restated(t)

	for _, tc := range []struct {
		name string
		got  []string
	}{
		{"Subjects", s[0].Subjects},
		{"Tags", s[0].Tags},
		{"Languages", s[0].Languages},
		{"Series", s[0].Series},
	} {
		if len(tc.got) != 1 {
			t.Errorf("%s = %v, want exactly one value", tc.name, tc.got)
		}
	}
}
