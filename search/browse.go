// Browse artifacts for the client-side reader (tasks/158). Alongside the
// per-language search indexes (search.go), the build emits a single global
// doc-id space (one entry per Work, in catalog order) as two RoaringRange
// sidecars the Hugo WASM reader opens directly:
//
//   - browse-facets.rrsf  -- an RRSF facet sidecar (language/format/subject/
//     tag/classification/contributor -> doc-id postings), for client-side facet
//     counts and filtering without pre-rendered facet pages.
//   - browse-records.bin/.idx -- an RRSR record store mapping a doc id to its
//     compact result-card JSON, for rendering result rows without a page load.
//
// browse-docs.json maps each doc id to its Work id, so the per-language search
// hits (which carry Work ids via their own docs maps) bridge into this global
// space, and a card links to the static /works/<id> detail page.
// browse-subjects.json maps each subject id to its labels + vocabulary scheme,
// so the fallback facet panel renders localized, scheme-grouped subjects
// (tasks/173).
package search

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/freeeve/libcat/project"
	"github.com/freeeve/libcat/storage"
	rr "github.com/freeeve/roaringrange"
)

// Browse artifact filenames.
const (
	BrowseIndexName    = "browse-index.rrs"
	BrowseRecordsBin   = "browse-records.bin"
	BrowseRecordsIdx   = "browse-records.idx"
	BrowseFacetsName   = "browse-facets.rrsf"
	BrowseDocsName     = "browse-docs.json"
	BrowseSubjectsName = "browse-subjects.json"
)

// Facet field names in the RRSF sidecar; the reader's filter pairs
// ([field, category]) and the Hugo facet UI key on these.
const (
	FacetLanguage       = "language"
	FacetFormat         = "format"
	FacetSubject        = "subject"
	FacetTag            = "tag"
	FacetClassification = "classification"
	FacetContributor    = "contributor"
)

// browseSubject is one subject category's display metadata in
// browse-subjects.json: the RRSF sidecar keys subjects by authority id only,
// so the fallback facet panel needs this map to render localized labels and
// group by vocabulary scheme like the static sidebar does (tasks/173).
type browseSubject struct {
	Labels map[string]string `json:"labels,omitempty"`
	Scheme string            `json:"scheme,omitempty"`
}

// browseCard is the compact per-Work payload stored in the record store -- what a
// search/browse result row needs; the row links to the static detail page for
// the rest.
type browseCard struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Subtitle     string   `json:"subtitle,omitempty"`
	Contributors []string `json:"contributors,omitempty"`
	Formats      []string `json:"formats,omitempty"`
	Held         bool     `json:"held,omitempty"`
	Cover        string   `json:"cover,omitempty"`
}

// BuildBrowse emits the facet sidecar, record store, and doc-id->Work-id map over
// a global doc-id space dense from 0 in catalog order.
func BuildBrowse(cat *project.Catalog, sink storage.Sink) error {
	records := make([][]byte, len(cat.Works))
	docIDs := make([]string, len(cat.Works))
	facets := map[string]map[string]*roaring.Bitmap{}
	subjects := map[string]browseSubject{}
	add := func(field, category string, doc uint32) {
		if category == "" {
			return
		}
		cats := facets[field]
		if cats == nil {
			cats = map[string]*roaring.Bitmap{}
			facets[field] = cats
		}
		bm := cats[category]
		if bm == nil {
			bm = roaring.New()
			cats[category] = bm
		}
		bm.Add(doc)
	}

	// A single global trigram index over all Works, in the same doc order as the
	// records and facets, so RrsCatalog.openAll ties search + facets + records
	// into one doc space (language-agnostic; the per-language stemmed indexes in
	// search.go remain for a future stemmed-search refinement).
	tri := rr.NewTrigramMonolithBuilder(trigramGramSize, 0)

	for i, w := range cat.Works {
		doc := uint32(i)
		docIDs[i] = w.ID
		tri.AddText(searchText(w))
		card := browseCard{ID: w.ID, Title: w.Title, Subtitle: w.Subtitle, Formats: w.Formats, Held: w.Held, Cover: w.Extra["cover"]}
		for _, c := range w.Contributors {
			card.Contributors = append(card.Contributors, c.Name)
			add(FacetContributor, c.Name, doc)
		}
		rec, err := json.Marshal(card)
		if err != nil {
			return fmt.Errorf("marshal browse card %s: %w", w.ID, err)
		}
		records[i] = rec
		for _, l := range w.Languages {
			add(FacetLanguage, l, doc)
		}
		for _, f := range w.Formats {
			add(FacetFormat, f, doc)
		}
		for _, s := range w.Subjects {
			add(FacetSubject, s.ID, doc)
			if _, ok := subjects[s.ID]; !ok {
				subjects[s.ID] = browseSubject{Labels: s.Labels, Scheme: s.Scheme}
			}
		}
		for _, t := range w.Tags {
			add(FacetTag, t, doc)
		}
		for _, c := range w.Classifications {
			add(FacetClassification, c.Value, doc)
		}
	}

	if err := writeTrigram(sink, BrowseIndexName, tri); err != nil {
		return err
	}
	if err := writeRecords(sink, records); err != nil {
		return err
	}
	if err := writeFacets(sink, facets); err != nil {
		return err
	}
	if err := writeJSON(sink, BrowseSubjectsName, subjects); err != nil {
		return err
	}
	return writeJSON(sink, BrowseDocsName, docIDs)
}

// writeRecords seals the per-Work cards into an RRSR record store (.bin blob +
// .idx offset index). Both writers are buffered: WriteRecords emits one small
// blob write plus an 8-byte offset write per record.
func writeRecords(sink storage.Sink, records [][]byte) error {
	bin, err := sink.Create(BrowseRecordsBin)
	if err != nil {
		return err
	}
	idx, err := sink.Create(BrowseRecordsIdx)
	if err != nil {
		bin.Close()
		return err
	}
	binW, idxW := bufio.NewWriter(bin), bufio.NewWriter(idx)
	if err := rr.WriteRecords(binW, idxW, records); err != nil {
		bin.Close()
		idx.Close()
		return fmt.Errorf("write record store: %w", err)
	}
	if err := binW.Flush(); err != nil {
		bin.Close()
		idx.Close()
		return err
	}
	if err := idxW.Flush(); err != nil {
		bin.Close()
		idx.Close()
		return err
	}
	if err := bin.Close(); err != nil {
		idx.Close()
		return err
	}
	return idx.Close()
}

// writeFacets seals the accumulated postings into an RRSF facet sidecar, fields
// and categories in sorted order so the artifact is deterministic.
func writeFacets(sink storage.Sink, facets map[string]map[string]*roaring.Bitmap) error {
	fields := make([]rr.FacetField, 0, len(facets))
	for _, fn := range slices.Sorted(maps.Keys(facets)) {
		cats := facets[fn]
		ff := rr.FacetField{Name: fn}
		for _, cn := range slices.Sorted(maps.Keys(cats)) {
			ff.Categories = append(ff.Categories, rr.FacetCategory{Name: cn, Bitmap: cats[cn]})
		}
		fields = append(fields, ff)
	}
	w, err := sink.Create(BrowseFacetsName)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	if err := rr.WriteFacets(bw, fields); err != nil {
		w.Close()
		return fmt.Errorf("write facet sidecar: %w", err)
	}
	if err := bw.Flush(); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}
