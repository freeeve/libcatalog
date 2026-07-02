// Package search builds the catalog's lexical search index from the projected
// catalog (ARCHITECTURE §8). Per corpus language it emits a roaringrange term
// index (.rrt, boolean whole-word presence) paired with a BM25 impact sidecar
// (.rrb) for relevance ranking, plus a manifest routing each language to its
// index -- the data the browser's WASM reader queries. Building is the only half
// done in Go: roaringrange has no Go term-index reader, so queries run in the
// Rust/WASM reader the Hugo module ships.
package search

import (
	"bufio"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/freeeve/libcatalog/project"
	"github.com/freeeve/libcatalog/storage"
	rr "github.com/freeeve/roaringrange"
)

// SchemaVersion is the search-manifest schema version, checked by the reader. v2
// adds the per-language BM25 impact sidecar (IndexInfo.Impacts).
const SchemaVersion = 2

// undetermined is the index key for Works with no declared language.
const undetermined = "und"

// Manifest is the language->index routing map the browser reader loads: one entry
// per language present in the corpus (§8).
type Manifest struct {
	Version int         `json:"version"`
	Indexes []IndexInfo `json:"indexes"`
}

// IndexInfo describes one per-language term index and how it was tokenized, so
// the reader tokenizes queries identically.
type IndexInfo struct {
	Language     string `json:"language"`     // ISO 639-2 code, or "und" when undeclared
	TermLanguage uint8  `json:"termLanguage"` // roaringrange stemmer-language byte
	Stemmed      bool   `json:"stemmed"`
	Stopwords    bool   `json:"stopwords"`
	Index        string `json:"index"`   // .rrt filename
	Impacts      string `json:"impacts"` // .rrb BM25 impact sidecar filename
	Docs         string `json:"docs"`    // JSON array: doc id (index) -> Work id
	DocCount     int    `json:"docCount"`
}

// BuildIndexes writes one term index (+ its BM25 sidecar) per corpus language
// into sink, plus a doc-id->Work-id list per index and a search-manifest.json
// routing map. A Work is indexed once per language it declares; a Work with none
// goes to the undetermined index. Doc ids are dense from 0 in the projected
// (sorted) order.
func BuildIndexes(cat *project.Catalog, sink storage.Sink) (Manifest, error) {
	byLang := map[string][]project.Work{}
	for _, w := range cat.Works {
		if len(w.Languages) == 0 {
			byLang[undetermined] = append(byLang[undetermined], w)
			continue
		}
		for _, l := range w.Languages {
			byLang[l] = append(byLang[l], w)
		}
	}

	langs := make([]string, 0, len(byLang))
	for l := range byLang {
		langs = append(langs, l)
	}
	slices.Sort(langs)

	m := Manifest{Version: SchemaVersion}
	for _, lang := range langs {
		info, err := buildLangIndex(sink, lang, byLang[lang])
		if err != nil {
			return m, err
		}
		m.Indexes = append(m.Indexes, info)
	}
	if err := writeJSON(sink, "search-manifest.json", m); err != nil {
		return m, err
	}
	return m, nil
}

// buildLangIndex builds and writes the term index and its BM25 sidecar for one
// language group. The presence postings (for the .rrt) and the impact statistics
// (per-doc length + term frequency, for the .rrb) are gathered over the same
// tokenizer so both address the identical vocabulary and dense doc-id order.
func buildLangIndex(sink storage.Sink, lang string, works []project.Work) (IndexInfo, error) {
	tl, stem := termLanguage(lang)
	stopwords := tl != rr.TermLanguageNone
	tok := rr.NewTermTokenizerFull(tl, stem, stopwords, true)
	acc := rr.NewImpactsAccumulator(tok)

	postings := map[string]*roaring.Bitmap{}
	docIDs := make([]string, len(works))
	for i, w := range works {
		docIDs[i] = w.ID
		text := searchText(w)
		acc.AddDoc(text) // records doc length + term frequencies for BM25; doc id == i
		terms := tok.Tokenize(text)
		slices.Sort(terms)
		terms = slices.Compact(terms) // the .rrt is a presence index; BM25 tf lives in the sidecar
		for _, t := range terms {
			bm := postings[t]
			if bm == nil {
				bm = roaring.New()
				postings[t] = bm
			}
			bm.Add(uint32(i))
		}
	}

	idxName := "term-" + lang + ".rrt"
	sidecarName := "term-" + lang + ".rrb"
	docsName := "term-" + lang + ".docs.json"
	dict, err := writeTermIndex(sink, idxName, postings, tl, stem, stopwords)
	if err != nil {
		return IndexInfo{}, err
	}
	if err := writeImpacts(sink, sidecarName, dict, acc); err != nil {
		return IndexInfo{}, err
	}
	if err := writeJSON(sink, docsName, docIDs); err != nil {
		return IndexInfo{}, err
	}
	return IndexInfo{
		Language:     lang,
		TermLanguage: uint8(tl),
		Stemmed:      stem,
		Stopwords:    stopwords,
		Index:        idxName,
		Impacts:      sidecarName,
		Docs:         docsName,
		DocCount:     len(works),
	}, nil
}

// writeTermIndex writes the boolean whole-word term index (.rrt) and returns its
// dictionary (terms in ascending posting head-offset order) so the paired BM25
// sidecar can address the exact postings written here. headBoundary is the default
// 65536; blockCap 0 selects roaringrange's default dict block size.
func writeTermIndex(sink storage.Sink, name string, postings map[string]*roaring.Bitmap, tl rr.TermLanguage, stem, stopwords bool) ([]rr.DictEntry, error) {
	w, err := sink.Create(name)
	if err != nil {
		return nil, err
	}
	dict, err := rr.WriteTermIndexFullDict(w, postings, 65536, tl, stem, stopwords, true, 0)
	if err != nil {
		w.Close()
		return nil, fmt.Errorf("write term index %s: %w", name, err)
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return dict, nil
}

// writeImpacts writes the BM25 impact sidecar (.rrb) that ranks the paired .rrt,
// using roaringrange's default k1/b (stored in the sidecar header for the reader).
// WriteImpacts emits many small writes, so the sink writer is buffered and flushed
// before close.
func writeImpacts(sink storage.Sink, name string, dict []rr.DictEntry, acc *rr.ImpactsAccumulator) error {
	w, err := sink.Create(name)
	if err != nil {
		return err
	}
	bw := bufio.NewWriter(w)
	if err := rr.WriteImpacts(bw, dict, acc, rr.DefaultK1, rr.DefaultB); err != nil {
		w.Close()
		return fmt.Errorf("write BM25 sidecar %s: %w", name, err)
	}
	if err := bw.Flush(); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// searchText is the text a Work contributes to the index: its title, subtitle,
// contributor names, controlled-subject labels (all languages), and feed tags.
// Term order does not matter -- the index sorts and dedups tokens -- but label
// values are gathered in sorted order so the text is deterministic.
func searchText(w project.Work) string {
	var b strings.Builder
	b.WriteString(w.Title)
	if w.Subtitle != "" {
		b.WriteByte(' ')
		b.WriteString(w.Subtitle)
	}
	for _, c := range w.Contributors {
		b.WriteByte(' ')
		b.WriteString(c.Name)
	}
	for _, s := range w.Subjects {
		labels := make([]string, 0, len(s.Labels))
		for _, l := range s.Labels {
			labels = append(labels, l)
		}
		slices.Sort(labels)
		for _, l := range labels {
			b.WriteByte(' ')
			b.WriteString(l)
		}
	}
	for _, t := range w.Tags {
		b.WriteByte(' ')
		b.WriteString(t)
	}
	return b.String()
}

// termLanguage maps an ISO 639-2 language code to a roaringrange stemmer language
// and whether stemming is applied. roaringrange (v0.27.0, its task 073) wires a
// Snowball stemmer on the Go build side for all 18 supported languages, so every
// mapped language is stemmed; an unmapped language indexes word-level with no stop
// words (see iso639).
func termLanguage(iso string) (rr.TermLanguage, bool) {
	if tl, ok := iso639[iso]; ok {
		return tl, tl != rr.TermLanguageNone
	}
	return rr.TermLanguageNone, false
}

// iso639 maps ISO 639-2 codes to roaringrange's 18 Snowball stemmer languages.
var iso639 = map[string]rr.TermLanguage{
	"eng": rr.TermLanguageEnglish, "spa": rr.TermLanguageSpanish, "ara": rr.TermLanguageArabic,
	"dan": rr.TermLanguageDanish, "dut": rr.TermLanguageDutch, "nld": rr.TermLanguageDutch,
	"fin": rr.TermLanguageFinnish, "fre": rr.TermLanguageFrench, "fra": rr.TermLanguageFrench,
	"ger": rr.TermLanguageGerman, "deu": rr.TermLanguageGerman, "gre": rr.TermLanguageGreek,
	"ell": rr.TermLanguageGreek, "hun": rr.TermLanguageHungarian, "ita": rr.TermLanguageItalian,
	"nor": rr.TermLanguageNorwegian, "por": rr.TermLanguagePortuguese, "rum": rr.TermLanguageRomanian,
	"ron": rr.TermLanguageRomanian, "rus": rr.TermLanguageRussian, "swe": rr.TermLanguageSwedish,
	"tam": rr.TermLanguageTamil, "tur": rr.TermLanguageTurkish,
}

// writeJSON marshals v as indented JSON through the sink.
func writeJSON(sink storage.Sink, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	w, err := sink.Create(name)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}
