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
// so the facet UI needs this map to render localized labels, group by
// vocabulary scheme like the static sidebar does (tasks/173), and nest
// concepts under their skos:broader parents (tasks/174).
type browseSubject struct {
	Labels  map[string]string `json:"labels,omitempty"`
	Scheme  string            `json:"scheme,omitempty"`
	Broader []string          `json:"broader,omitempty"`
	// Minted marks an entry created only to close an ancestry hole
	// (expandSubjectAncestry): no Work carries it directly. Its labels and
	// broader edges fill from the catalog's vocabulary sideband when the
	// graph described the term (tasks/178); while it stays label-less the
	// facet UI keeps its rolled-up postings out of the rendered tree instead
	// of showing a raw authority URI as a top-level concept (tasks/176).
	Minted bool `json:"minted,omitempty"`
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

	terms := make(map[string]project.Term, len(cat.Terms))
	for _, t := range cat.Terms {
		terms[t.ID] = t
	}

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
				subjects[s.ID] = browseSubject{Labels: s.Labels, Scheme: s.Scheme, Broader: s.Broader}
			}
		}
		for _, t := range w.Tags {
			add(FacetTag, t, doc)
		}
		for _, c := range w.Classifications {
			add(FacetClassification, c.Value, doc)
		}
	}

	expandSubjectAncestry(facets[FacetSubject], subjects, terms)

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

// ancestryDepthCap bounds the skos:broader walk; homosaurus tops out well
// under this, and a malformed vocabulary must not spin the build.
const ancestryDepthCap = 12

// expandSubjectAncestry unions each subject category's postings into every
// skos:broader ancestor's postings (tasks/174, mirroring the QLL POC): a
// parent concept's count then already rolls up its subtree, and a filter on
// the parent -- include or exclude -- covers works tagged anywhere below it,
// with no per-node queries client-side. Ancestors named by broader edges but
// never used as a direct subject are minted into both the postings and the
// metadata map, flagged Minted so the UI can keep label-less plumbing nodes
// out of the rendered tree. A minted entry fills its labels, broader edges,
// and scheme from the catalog's vocabulary sideband when the graph described
// the term (tasks/178) -- labels make it a real tree node client-side, and
// its broader edges extend the walk so rollups cross ancestors no work
// carries; without a sideband entry the scheme falls back to the child's.
func expandSubjectAncestry(cats map[string]*roaring.Bitmap, subjects map[string]browseSubject, terms map[string]project.Term) {
	if len(cats) == 0 {
		return
	}
	for _, id := range slices.Sorted(maps.Keys(cats)) {
		bm := cats[id]
		seen := map[string]bool{id: true}
		frontier := subjects[id].Broader
		for depth := 0; depth < ancestryDepthCap && len(frontier) > 0; depth++ {
			var next []string
			for _, a := range frontier {
				if a == "" || seen[a] {
					continue
				}
				seen[a] = true
				if _, ok := subjects[a]; !ok {
					minted := browseSubject{Scheme: subjects[id].Scheme, Minted: true}
					if t, ok := terms[a]; ok {
						minted.Labels = t.Labels
						minted.Broader = t.Broader
						if t.Scheme != "" {
							minted.Scheme = t.Scheme
						}
					}
					subjects[a] = minted
				}
				abm := cats[a]
				if abm == nil {
					abm = roaring.New()
					cats[a] = abm
				}
				abm.Or(bm)
				next = append(next, subjects[a].Broader...)
			}
			frontier = next
		}
	}
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
