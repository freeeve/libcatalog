package search

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/freeeve/libcat/project"
	rr "github.com/freeeve/roaringrange"
)

// TestBuildBrowse checks the browse artifacts round-trip: the record store reads
// back the per-Work cards, the doc map aligns doc ids to Work ids, and the facet
// sidecar carries the RRSF header.
func TestBuildBrowse(t *testing.T) {
	cat := &project.Catalog{Works: []project.Work{
		{
			ID: "w1", Title: "A Wizard of Earthsea", Languages: []string{"eng"}, Formats: []string{"book"},
			Contributors: []project.Contributor{{Name: "Le Guin, Ursula K."}},
			Subjects:     []project.Subject{{ID: "s:fantasy"}}, Tags: []string{"fantasy"},
		},
		{
			ID: "w2", Title: "The Tombs of Atuan", Languages: []string{"eng"}, Formats: []string{"ebook"},
			Contributors: []project.Contributor{{Name: "Le Guin, Ursula K."}},
			Subjects:     []project.Subject{{ID: "s:fantasy"}}, Extra: map[string]string{"cover": "/img/w2.jpg"},
		},
	}}
	sink := newMemSink()
	if err := BuildBrowse(cat, sink); err != nil {
		t.Fatal(err)
	}

	for _, f := range []string{BrowseIndexName, BrowseRecordsBin, BrowseRecordsIdx, BrowseFacetsName, BrowseDocsName} {
		if _, ok := sink.files[f]; !ok {
			t.Fatalf("missing artifact %s", f)
		}
	}

	// Doc map aligns doc id -> Work id in catalog order.
	var docs []string
	if err := json.Unmarshal(sink.files[BrowseDocsName], &docs); err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0] != "w1" || docs[1] != "w2" {
		t.Fatalf("browse docs = %v", docs)
	}

	// The record store reads back the card for doc id 1 (== w2).
	rs, err := rr.OpenRecordStore(bytes.NewReader(sink.files[BrowseRecordsIdx]), bytes.NewReader(sink.files[BrowseRecordsBin]))
	if err != nil {
		t.Fatal(err)
	}
	if rs.Len() != 2 {
		t.Fatalf("record count = %d, want 2", rs.Len())
	}
	data, ok, err := rs.Get(1)
	if err != nil || !ok {
		t.Fatalf("get doc 1: ok=%v err=%v", ok, err)
	}
	var card browseCard
	if err := json.Unmarshal(data, &card); err != nil {
		t.Fatal(err)
	}
	if card.ID != "w2" || card.Cover != "/img/w2.jpg" || len(card.Formats) != 1 || card.Formats[0] != "ebook" {
		t.Fatalf("card doc 1 = %+v", card)
	}

	// The facet sidecar carries the RRSF header.
	f := sink.files[BrowseFacetsName]
	if len(f) < len(rr.MagicFacet) || string(f[:len(rr.MagicFacet)]) != rr.MagicFacet {
		t.Fatalf("facet sidecar not RRSF (len %d)", len(f))
	}
}

// TestBuildBrowseSubjectAncestry checks tasks/174: subject postings roll up
// through skos:broader (a parent category covers its subtree), an ancestor
// never used as a direct subject is minted with the child's scheme, and
// browse-subjects.json carries labels, scheme, and broader edges.
func TestBuildBrowseSubjectAncestry(t *testing.T) {
	cat := &project.Catalog{Works: []project.Work{
		{ID: "w1", Title: "Parent-tagged", Subjects: []project.Subject{
			{ID: "s:parent", Labels: map[string]string{"en": "Gender minorities"}, Scheme: "homosaurus", Broader: []string{"s:grand"}},
		}},
		{ID: "w2", Title: "Child-tagged", Subjects: []project.Subject{
			{ID: "s:child", Labels: map[string]string{"en": "Trans women"}, Scheme: "homosaurus", Broader: []string{"s:parent"}},
		}},
		{ID: "w3", Title: "Flat-tagged", Subjects: []project.Subject{
			{ID: "f:flat", Labels: map[string]string{"en": "Fiction"}, Scheme: "fast"},
		}},
	}}
	sink := newMemSink()
	if err := BuildBrowse(cat, sink); err != nil {
		t.Fatal(err)
	}

	fi, err := rr.OpenFacets(bytes.NewReader(sink.files[BrowseFacetsName]))
	if err != nil {
		t.Fatal(err)
	}
	fields, err := fi.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]uint64{}
	for _, fld := range fields {
		if fld.Name != FacetSubject {
			continue
		}
		for _, c := range fld.Categories {
			counts[c.Name] = c.Bitmap.GetCardinality()
		}
	}
	want := map[string]uint64{"s:child": 1, "s:parent": 2, "s:grand": 2, "f:flat": 1}
	for id, n := range want {
		if counts[id] != n {
			t.Fatalf("posting count %s = %d, want %d (all: %v)", id, counts[id], n, counts)
		}
	}

	var subjects map[string]browseSubject
	if err := json.Unmarshal(sink.files[BrowseSubjectsName], &subjects); err != nil {
		t.Fatal(err)
	}
	if s := subjects["s:child"]; s.Scheme != "homosaurus" || s.Labels["en"] != "Trans women" || len(s.Broader) != 1 || s.Broader[0] != "s:parent" {
		t.Fatalf("child meta = %+v", s)
	}
	if s := subjects["s:grand"]; s.Scheme != "homosaurus" || len(s.Labels) != 0 || !s.Minted {
		t.Fatalf("minted ancestor meta = %+v", s)
	}
	if s := subjects["f:flat"]; s.Scheme != "fast" || len(s.Broader) != 0 || s.Minted {
		t.Fatalf("flat meta = %+v", s)
	}
	if s := subjects["s:parent"]; s.Minted {
		t.Fatalf("direct subject flagged minted: %+v", s)
	}
}

// TestBuildBrowseTermSideband checks tasks/178: a minted ancestor fills its
// labels, broader edges, and scheme from Catalog.Terms, and those broader
// edges extend the rollup walk across ancestors no work carries -- the
// postings reach a grandparent known only to the sideband.
func TestBuildBrowseTermSideband(t *testing.T) {
	cat := &project.Catalog{
		Works: []project.Work{
			{ID: "w1", Title: "Child-tagged", Subjects: []project.Subject{
				{ID: "s:child", Labels: map[string]string{"en": "Trans women"}, Scheme: "homosaurus", Broader: []string{"s:parent"}},
			}},
		},
		Terms: []project.Term{
			{ID: "s:parent", Labels: map[string]string{"en": "Gender minorities"}, Broader: []string{"s:grand"}, Scheme: "homosaurus"},
			{ID: "s:grand", Broader: []string{"s:great"}},
			{ID: "s:great", Labels: map[string]string{"en": "People"}},
		},
	}
	sink := newMemSink()
	if err := BuildBrowse(cat, sink); err != nil {
		t.Fatal(err)
	}

	fi, err := rr.OpenFacets(bytes.NewReader(sink.files[BrowseFacetsName]))
	if err != nil {
		t.Fatal(err)
	}
	fields, err := fi.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]uint64{}
	for _, fld := range fields {
		if fld.Name != FacetSubject {
			continue
		}
		for _, c := range fld.Categories {
			counts[c.Name] = c.Bitmap.GetCardinality()
		}
	}
	for _, id := range []string{"s:child", "s:parent", "s:grand", "s:great"} {
		if counts[id] != 1 {
			t.Fatalf("posting count %s = %d, want 1 (all: %v)", id, counts[id], counts)
		}
	}

	var subjects map[string]browseSubject
	if err := json.Unmarshal(sink.files[BrowseSubjectsName], &subjects); err != nil {
		t.Fatal(err)
	}
	if s := subjects["s:parent"]; !s.Minted || s.Labels["en"] != "Gender minorities" || s.Scheme != "homosaurus" || len(s.Broader) != 1 || s.Broader[0] != "s:grand" {
		t.Fatalf("labeled minted parent meta = %+v", s)
	}
	// s:grand has no sideband labels and no explicit scheme: minted with the
	// child's scheme inherited, broader carried from the sideband.
	if s := subjects["s:grand"]; !s.Minted || len(s.Labels) != 0 || s.Scheme != "homosaurus" || len(s.Broader) != 1 || s.Broader[0] != "s:great" {
		t.Fatalf("plumbing grandparent meta = %+v", s)
	}
	if s := subjects["s:great"]; !s.Minted || s.Labels["en"] != "People" {
		t.Fatalf("sideband-labeled great-grandparent meta = %+v", s)
	}
}
