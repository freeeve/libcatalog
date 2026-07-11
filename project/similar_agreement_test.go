package project

import (
	"slices"
	"testing"

	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/similar"
	"github.com/freeeve/libcodex/rdf"
)

// The OPAC precomputes "more like this" at build time from project.Work; the admin
// endpoint scores it live from ingest.WorkSummary. If the two converters disagree
// about what a Work carries, the same book gets different neighbours on the two
// surfaces -- a bug a reader can see, a cataloger cannot reproduce, and no unit
// test over hand-written inputs can find. That is the failure class:
// two readers of one truth, drifting.
//
// So: one graph, both converters, and the results must agree. Values are compared
// as sets because similar.Build normalizes; a difference in *content* is the bug.
// agreementNQBase carries every signal except the series, which the two fixtures
// below add in the two shapes the readers must both handle.
const agreementNQBase = `<#waWork> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Work> <feed:overdrive> .
<#waWork> <http://www.w3.org/2000/01/rdf-schema#label> "Herculine" <feed:overdrive> .
<#waWork> <http://id.loc.gov/ontologies/bibframe/subject> <https://homosaurus.org/v3/homoit0000123> <feed:overdrive> .
<#waWork> <http://id.loc.gov/ontologies/bibframe/subject> <https://id.loc.gov/authorities/subjects/sh85078196> <feed:overdrive> .
<#waWork> <http://id.loc.gov/ontologies/bibframe/subject> _:tagnode <feed:overdrive> .
_:tagnode <http://www.w3.org/2000/01/rdf-schema#label> "queer fiction" <feed:overdrive> .
<#waWork> <https://github.com/freeeve/libcat/ns#tag> "gothic" <feed:overdrive> .
<#waWork> <http://id.loc.gov/ontologies/bibframe/language> <http://id.loc.gov/vocabulary/languages/eng> <feed:overdrive> .
<#waWork> <http://id.loc.gov/ontologies/bibframe/contribution> _:contribA <feed:overdrive> .
_:contribA <http://id.loc.gov/ontologies/bibframe/agent> _:agentA <feed:overdrive> .
_:agentA <http://www.w3.org/2000/01/rdf-schema#label> "Winterson, Jeanette" <feed:overdrive> .
<#waWork> <http://id.loc.gov/ontologies/bibframe/hasInstance> <#waInstance> <feed:overdrive> .
<#waInstance> <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Instance> <feed:overdrive> .
<#waInstance> <http://id.loc.gov/ontologies/bibframe/identifiedBy> _:reserveA <feed:overdrive> .
_:reserveA <http://www.w3.org/1999/02/22-rdf-syntax-ns#value> "24760f5d" <feed:overdrive> .
_:reserveA <http://id.loc.gov/ontologies/bibframe/source> _:srcA <feed:overdrive> .
_:srcA <http://www.w3.org/2000/01/rdf-schema#label> "overdrive-reserve" <feed:overdrive> .
`

// agreementNQ is the pre-v0.25.0 shape: a flat seriesStatement literal on the
// Instance. Both converters keep a fallback for it, so archived grains and any
// corpus not yet re-ingested still carry their series.
const agreementNQ = agreementNQBase + `<#waInstance> <http://id.loc.gov/ontologies/bibframe/seriesStatement> "The Cornish Trilogy" <feed:overdrive> .
`

func sorted(vs []string) []string {
	out := slices.Clone(vs)
	slices.Sort(out)
	return slices.Compact(out)
}

// sameSet compares two attribute slices the way similar.Build consumes them.
func sameSet(t *testing.T, field string, admin, opac []string) {
	t.Helper()
	if a, o := sorted(admin), sorted(opac); !slices.Equal(a, o) {
		t.Errorf("%s disagrees:\n  admin (ingest.WorkSummary) = %v\n  opac  (project.Work)      = %v", field, a, o)
	}
}

// agreementRelationNQ is the same Work with its series in the shape libcodex
// v0.25.0 emits: a bf:relation on the Work rather than a flat literal
// on the Instance. Both converters grew a new reader for it, and both kept the old
// one for archived grains, so both shapes have to be driven through both readers
// -- otherwise the pair agrees on legacy graphs and silently diverges on new ones.
const agreementRelationNQ = agreementNQBase + `<#waWork> <http://id.loc.gov/ontologies/bibframe/relation> _:rel <feed:overdrive> .
_:rel <http://id.loc.gov/ontologies/bibframe/relationship> <http://id.loc.gov/vocabulary/relationship/series> <feed:overdrive> .
_:rel <http://id.loc.gov/ontologies/bibframe/seriesEnumeration> "bk. 1" <feed:overdrive> .
_:rel <http://id.loc.gov/ontologies/bibframe/associatedResource> _:series <feed:overdrive> .
_:series <http://www.w3.org/1999/02/22-rdf-syntax-ns#type> <http://id.loc.gov/ontologies/bibframe/Series> <feed:overdrive> .
_:series <http://id.loc.gov/ontologies/bibframe/title> _:seriesTitle <feed:overdrive> .
_:seriesTitle <http://id.loc.gov/ontologies/bibframe/mainTitle> "The Cornish Trilogy" <feed:overdrive> .
`

func TestBothConvertersAgreeOnTheSameGraph(t *testing.T) {
	for name, nq := range map[string]string{
		"legacy flat literals": agreementNQ,
		"v0.25.0 relations":    agreementRelationNQ,
	} {
		t.Run(name, func(t *testing.T) {
			cat, err := Project([]byte(nq), "overdrive")
			if err != nil {
				t.Fatalf("Project: %v", err)
			}
			if len(cat.Works) != 1 {
				t.Fatalf("projected %d works, want 1", len(cat.Works))
			}
			opac := cat.Works[0].SimilarWork()

			ds, err := rdf.ParseNQuadsShared([]byte(nq))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			summaries := ingest.SummarizeDataset(ds)
			if len(summaries) != 1 {
				t.Fatalf("summarized %d works, want 1", len(summaries))
			}
			admin := summaries[0].SimilarWork()

			if admin.WorkID != opac.WorkID {
				t.Errorf("WorkID disagrees: admin %q, opac %q", admin.WorkID, opac.WorkID)
			}
			if admin.Held != opac.Held {
				t.Errorf("Held disagrees: admin %v, opac %v", admin.Held, opac.Held)
			}
			sameSet(t, "Subjects", admin.Subjects, opac.Subjects)
			sameSet(t, "Contributors", admin.Contributors, opac.Contributors)
			sameSet(t, "Tags", admin.Tags, opac.Tags)
			sameSet(t, "Series", admin.Series, opac.Series)
			sameSet(t, "Languages", admin.Languages, opac.Languages)

			// Both shapes must name the same series, or "they agree" only means
			// "they are equally empty".
			if len(opac.Series) != 1 || opac.Series[0] != "The Cornish Trilogy" {
				t.Errorf("series = %v, want the fixture's one series", opac.Series)
			}
		})
	}
}

// The agreement above is worthless if the graph exercises no signal. Assert the
// fixture actually carries one of each, so a converter that silently drops a
// field cannot pass by both sides returning nil.
func TestAgreementFixtureExercisesEverySignal(t *testing.T) {
	cat, err := Project([]byte(agreementNQ), "overdrive")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	w := cat.Works[0].SimilarWork()
	for _, tc := range []struct {
		field string
		vs    []string
	}{
		{"Subjects", w.Subjects},
		{"Contributors", w.Contributors},
		{"Tags", w.Tags},
		{"Series", w.Series},
		{"Languages", w.Languages},
	} {
		if len(tc.vs) == 0 {
			t.Errorf("fixture carries no %s, so the agreement test cannot see that field", tc.field)
		}
	}
	if !w.Held {
		t.Error("fixture Work is not Held, so the availability signal is untested")
	}
}

// Tombstoned rides through the admin converter and is dropped by the scorer, not
// by the converter -- a retired record must not be recommended from elsewhere
// , and the admin list still has to display it.
func TestSimilarWorkKeepsSuppressedAndFlagsTombstoned(t *testing.T) {
	sup := ingest.WorkSummary{WorkID: "wsup", Suppressed: true, Subjects: []string{"s:1"}}
	dead := ingest.WorkSummary{WorkID: "wdead", Tombstoned: true, Subjects: []string{"s:1"}}
	live := ingest.WorkSummary{WorkID: "wlive", Subjects: []string{"s:1"}}

	if sup.SimilarWork().Tombstoned {
		t.Error("a suppressed Work was converted as tombstoned; the admin surface shows it")
	}
	if !dead.SimilarWork().Tombstoned {
		t.Error("a tombstoned Work lost its flag in conversion")
	}
	ix := similar.Build(ingest.SimilarWorks([]ingest.WorkSummary{sup, dead, live}), similar.DefaultOptions())
	if ix.Len() != 2 {
		t.Fatalf("indexed %d works, want 2 (the tombstoned one excluded)", ix.Len())
	}
	got := ix.Neighbors("wlive", 10)
	for _, s := range got {
		if s.WorkID == "wdead" {
			t.Fatal("a tombstoned Work was recommended")
		}
	}
	if len(got) != 1 || got[0].WorkID != "wsup" {
		t.Fatalf("neighbors = %v, want the suppressed work", got)
	}
}
