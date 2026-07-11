package hardcover

import (
	_ "embed"
	"encoding/json"
	"strings"

	"github.com/freeeve/libcat/bibframe"
)

// subjectMapJSON is the shipped genre->authority table (ported from the demo pipeline,
// ), embedded so the provider is data-driven without a runtime file dependency.
//
//go:embed subject-map.json
var subjectMapJSON []byte

// subjectTable maps lowercased genre tags to controlled-subject authority records. It is
// loaded once from the embedded table at package init.
type subjectTable struct {
	genreToURI map[string]string
	authority  map[string]authorityRecord
}

type authorityRecord struct {
	Labels  map[string]string `json:"labels"`
	Broader []string          `json:"broader"`
}

// subjects is the parsed genre->authority table.
var subjects = loadSubjectTable()

// loadSubjectTable parses the embedded table. A malformed table yields an empty table
// (all genres stay uncontrolled tags) rather than a panic, so a bad edit degrades
// gracefully; the golden test guards the shipped file.
func loadSubjectTable() subjectTable {
	var raw struct {
		GenreToSubject map[string]string          `json:"genreToSubject"`
		Authorities    map[string]authorityRecord `json:"authorities"`
	}
	_ = json.Unmarshal(subjectMapJSON, &raw)
	t := subjectTable{genreToURI: map[string]string{}, authority: raw.Authorities}
	for genre, uri := range raw.GenreToSubject {
		t.genreToURI[strings.ToLower(strings.TrimSpace(genre))] = uri
	}
	if t.authority == nil {
		t.authority = map[string]authorityRecord{}
	}
	return t
}

// lookup returns the authority URI a genre maps to, if any.
func (t subjectTable) lookup(genre string) (string, bool) {
	uri, ok := t.genreToURI[strings.ToLower(strings.TrimSpace(genre))]
	return uri, ok
}

// tags returns the book's genres that are NOT promoted to controlled subjects, so an
// uncontrolled tag and a controlled subject never duplicate the same genre (the demo's
// map-subjects behavior).
func (b book) tags() []string {
	var out []string
	for _, g := range b.genres() {
		if _, mapped := subjects.lookup(g); !mapped {
			out = append(out, g)
		}
	}
	return out
}

// controlledSubjects returns the controlled subjects for the book's mapped genres, each
// with the authority's localized labels and skos:broader parents, deduped by URI and in
// genre order. Empty when no genre maps.
func (b book) controlledSubjects() []bibframe.AuthoritySubject {
	var out []bibframe.AuthoritySubject
	seen := map[string]bool{}
	for _, g := range b.genres() {
		uri, ok := subjects.lookup(g)
		if !ok || seen[uri] {
			continue
		}
		seen[uri] = true
		rec := subjects.authority[uri]
		out = append(out, bibframe.AuthoritySubject{URI: uri, Labels: rec.Labels, Broader: rec.Broader})
	}
	return out
}
