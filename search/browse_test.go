package search

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/freeeve/libcatalog/project"
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

	for _, f := range []string{BrowseRecordsBin, BrowseRecordsIdx, BrowseFacetsName, BrowseDocsName} {
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
