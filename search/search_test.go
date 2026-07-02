package search

import (
	"encoding/json"
	"io"
	"reflect"
	"testing"

	"github.com/freeeve/libcatalog/project"
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
