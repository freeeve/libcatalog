package search

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/freeeve/libcat/project"
	rr "github.com/freeeve/roaringrange"
)

// memSink captures written files in memory for assertions.
type memSink struct{ files map[string][]byte }

func newMemSink() *memSink { return &memSink{files: map[string][]byte{}} }

func (m *memSink) Create(path string) (io.WriteCloser, error) {
	return &memFile{sink: m, path: path}, nil
}

type memFile struct {
	sink *memSink
	path string
	buf  []byte
}

func (f *memFile) Write(p []byte) (int, error) { f.buf = append(f.buf, p...); return len(p), nil }
func (f *memFile) Close() error                { f.sink.files[f.path] = f.buf; return nil }

func TestTermLanguage(t *testing.T) {
	cases := []struct {
		iso  string
		want rr.TermLanguage
		stem bool
	}{
		{"eng", rr.TermLanguageEnglish, true},
		{"spa", rr.TermLanguageSpanish, true}, // all 18 Snowball languages stem Go-side (rr task 073)
		{"fre", rr.TermLanguageFrench, true},
		{"zxx", rr.TermLanguageNone, false}, // unmapped -> word-level, no stemmer
		{"", rr.TermLanguageNone, false},
	}
	for _, c := range cases {
		tl, stem := termLanguage(c.iso)
		if tl != c.want || stem != c.stem {
			t.Errorf("termLanguage(%q) = %v,%v; want %v,%v", c.iso, tl, stem, c.want, c.stem)
		}
	}
}

func TestSearchText(t *testing.T) {
	got := searchText(project.Work{
		Title:        "Herculine",
		Subtitle:     "A Novel",
		Contributors: []project.Contributor{{Name: "Byron, Grace"}},
		Subjects:     []project.Subject{{ID: "https://homosaurus.org/v3/homoit0000669", Labels: map[string]string{"en": "Transgender people"}}},
		Tags:         []string{"Fiction"},
	})
	want := "Herculine A Novel Byron, Grace Transgender people Fiction"
	if got != want {
		t.Errorf("searchText = %q, want %q", got, want)
	}
}

func TestBuildIndexes(t *testing.T) {
	cat := &project.Catalog{Works: []project.Work{
		{ID: "w1", Title: "Herculine", Languages: []string{"eng"}},
		{ID: "w2", Title: "Sovereign", Languages: []string{"eng"}},
		{ID: "w3", Title: "La casa", Languages: []string{"spa"}},
		{ID: "w4", Title: "Untitled"}, // no language -> undetermined
	}}
	sink := newMemSink()
	m, err := BuildIndexes(cat, sink)
	if err != nil {
		t.Fatalf("BuildIndexes: %v", err)
	}
	if m.Version != SchemaVersion {
		t.Errorf("manifest version = %d, want %d", m.Version, SchemaVersion)
	}

	byLang := map[string]IndexInfo{}
	for _, ix := range m.Indexes {
		byLang[ix.Language] = ix
	}
	if len(byLang) != 3 {
		t.Fatalf("indexes = %v, want eng/spa/und", byLang)
	}
	if byLang["eng"].DocCount != 2 || byLang["spa"].DocCount != 1 || byLang["und"].DocCount != 1 {
		t.Errorf("doc counts = eng:%d spa:%d und:%d", byLang["eng"].DocCount, byLang["spa"].DocCount, byLang["und"].DocCount)
	}
	if !byLang["eng"].Stemmed || !byLang["spa"].Stemmed {
		t.Errorf("both eng and spa should stem Go-side: eng=%v spa=%v", byLang["eng"].Stemmed, byLang["spa"].Stemmed)
	}

	// The English index, its BM25 sidecar, and its doc map were written; the doc map
	// lists the eng Work ids in order, so a query result (doc id) maps back to a Work.
	if len(sink.files["term-eng.rrt"]) == 0 {
		t.Error("term-eng.rrt not written or empty")
	}
	if byLang["eng"].Impacts != "term-eng.rrb" || len(sink.files["term-eng.rrb"]) == 0 {
		t.Errorf("BM25 sidecar term-eng.rrb not written (impacts=%q, %d bytes)", byLang["eng"].Impacts, len(sink.files["term-eng.rrb"]))
	}
	if got := string(sink.files["term-eng.rrb"][:4]); got != "RRSB" {
		t.Errorf("sidecar magic = %q, want RRSB", got)
	}
	if byLang["eng"].Kind != kindTerms || byLang["spa"].Kind != kindTerms {
		t.Errorf("segmented-script indexes should be kind=terms: eng=%q spa=%q", byLang["eng"].Kind, byLang["spa"].Kind)
	}
	var docs []string
	if err := json.Unmarshal(sink.files["term-eng.docs.json"], &docs); err != nil {
		t.Fatalf("docs.json: %v", err)
	}
	if !reflect.DeepEqual(docs, []string{"w1", "w2"}) {
		t.Errorf("eng doc map = %v, want [w1 w2]", docs)
	}
	if _, ok := sink.files["search-manifest.json"]; !ok {
		t.Error("search-manifest.json not written")
	}
}

// TestTrigramRecall proves the CJK/unsegmented-script arm (tasks/005 item 3): a
// Chinese corpus routes to a trigram RRSI index, and a substring query's trigrams
// recall the doc that contains it (verified through roaringrange's Go RRSI reader,
// so no browser is needed). Word-level indexing would collapse each title into one
// token and miss the substring entirely.
func TestTrigramRecall(t *testing.T) {
	cat := &project.Catalog{Works: []project.Work{
		{ID: "c1", Title: "红楼梦", Languages: []string{"chi"}},
		{ID: "c2", Title: "三国演义", Languages: []string{"chi"}},
	}}
	sink := newMemSink()
	m, err := BuildIndexes(cat, sink)
	if err != nil {
		t.Fatalf("BuildIndexes: %v", err)
	}

	var info IndexInfo
	for _, ix := range m.Indexes {
		if ix.Language == "chi" {
			info = ix
		}
	}
	if info.Kind != kindTrigram || info.Index != "trigram-chi.rrs" || info.GramSize != trigramGramSize {
		t.Fatalf("chi routing = %+v, want kind=trigram index=trigram-chi.rrs gramSize=3", info)
	}
	if info.Impacts != "" || info.Stemmed {
		t.Errorf("trigram index carries no BM25 sidecar or stemming: %+v", info)
	}
	blob := sink.files[info.Index]
	if len(blob) < 4 || string(blob[:4]) != "RRSI" {
		t.Fatalf("trigram index magic = %q, want RRSI", blob[:min(4, len(blob))])
	}

	// Query "红楼梦" -> its trigram key(s) -> intersect postings -> must recall doc 0
	// (c1) and not doc 1 (c2), the recall the acceptance calls for.
	idx, err := rr.Open(bytes.NewReader(blob))
	if err != nil {
		t.Fatalf("open RRSI: %v", err)
	}
	keys := rr.NgramKeys("红楼梦", info.GramSize)
	if len(keys) == 0 {
		t.Fatal("query produced no trigram keys")
	}
	post, err := idx.Postings(keys)
	if err != nil {
		t.Fatalf("Postings: %v", err)
	}
	hits := intersect(t, keys, post)
	if !hits.Contains(0) {
		t.Errorf("query '红楼梦' did not recall doc 0 (c1); hits=%v", hits.ToArray())
	}
	if hits.Contains(1) {
		t.Errorf("query '红楼梦' wrongly matched doc 1 (三国演义)")
	}
}

// intersect decodes each query trigram's posting and ANDs them: a doc matches only
// if it contains every query trigram. A missing key yields the empty set.
func intersect(t *testing.T, keys []uint64, post map[uint64][]byte) *roaring.Bitmap {
	t.Helper()
	var acc *roaring.Bitmap
	for _, k := range keys {
		data, ok := post[k]
		if !ok {
			return roaring.New()
		}
		bm := roaring.New()
		if err := bm.UnmarshalBinary(data); err != nil {
			t.Fatalf("decode posting: %v", err)
		}
		if acc == nil {
			acc = bm
		} else {
			acc.And(bm)
		}
	}
	if acc == nil {
		return roaring.New()
	}
	return acc
}
