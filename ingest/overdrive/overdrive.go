// Package overdrive maps OverDrive/Libby "thunder" API records (as cached by a scan)
// directly to libcodex BIBFRAME Work/Instance grains (see bibframe.go). It is the
// ingest half of the OverDrive reference provider (ARCHITECTURE §9): each cached Item
// exposes Identity/Work/Instance for the shared ingest.Run pipeline, so a cached
// collection becomes canonical feed:overdrive grains with no MARC intermediate. The
// live fetch is a separate concern; this reads the on-disk page cache so a build needs
// no API call.
package overdrive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Item is the subset of an OverDrive media record this connector maps. Field
// names match the raw thunder feed (cache/pages/*.json).
type Item struct {
	ID          string    `json:"id"`        // OverDrive numeric title id
	ReserveID   string    `json:"reserveId"` // OverDrive Reserve ID (Thunder availability key)
	Title       string    `json:"title"`
	Subtitle    string    `json:"subtitle"`
	Edition     string    `json:"edition"`
	Series      string    `json:"series"`
	Description string    `json:"description"` // HTML fragment; plain-texted into bf:summary
	Type        NamedID   `json:"type"`        // {id: ebook|audiobook, name}
	Publisher   *NamedID  `json:"publisher"`
	PublishDate string    `json:"publishDate"` // ISO 8601
	Creators    []Creator `json:"creators"`
	Languages   []NamedID `json:"languages"`
	Subjects    []NamedID `json:"subjects"`
	BISAC       []BISAC   `json:"bisac"`
	Formats     []Format  `json:"formats"`
}

// NamedID is OverDrive's {id, name} pair (types, subjects, languages, publisher).
type NamedID struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Creator is a contributor with an OverDrive role and an authorized sort form.
type Creator struct {
	Name     string `json:"name"`
	Role     string `json:"role"` // Author, Narrator, Illustrator, Editor, Translator
	SortName string `json:"sortName"`
}

// BISAC is a controlled subject classification.
type BISAC struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

// Format is one delivery format; ISBNs ride on its identifiers.
type Format struct {
	Identifiers []Identifier `json:"identifiers"`
}

// Identifier is a typed identifier (ISBN, ASIN, ...) on a format.
type Identifier struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type page struct {
	Items []Item `json:"items"`
}

// ReadCache reads every page-*.json in dir (the OverDrive scan cache) and returns
// all items across the pages, in page order. A dir with no page files is an
// error, not an empty feed: a mistyped --cache path must not read as "the
// provider now carries zero titles".
func ReadCache(dir string) ([]Item, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("overdrive cache %s: %w", dir, err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "page-*.json"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("overdrive cache %s: no page-*.json files", dir)
	}
	sort.Strings(matches)
	var items []Item
	for _, m := range matches {
		b, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", m, err)
		}
		var p page
		if err := json.Unmarshal(b, &p); err != nil {
			return nil, fmt.Errorf("parse %s: %w", m, err)
		}
		items = append(items, p.Items...)
	}
	return items, nil
}

// contributor is a name field's transcribed form, relationship term, and relator.
type contributor struct {
	name, role, relator string
}

// contributors splits creators into author entries and other roles, resolving each
// role to its lowercased term and MARC relator code (author -> aut, narrator -> nrt).
// Both the direct BIBFRAME path (bibframe.go) and identity clustering read these.
func (it Item) contributors() (authors, others []contributor) {
	for _, c := range it.Creators {
		name := c.SortName
		if name == "" {
			name = c.Name
		}
		con := contributor{name: name, role: strings.ToLower(c.Role), relator: relatorCode(c.Role)}
		if c.Role == "Author" {
			authors = append(authors, con)
		} else {
			others = append(others, con)
		}
	}
	return authors, others
}

// ISBNs returns the deduped ISBNs across all formats, in first-seen order.
func (it Item) ISBNs() []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range it.Formats {
		for _, id := range f.Identifiers {
			if id.Type == "ISBN" && id.Value != "" && !seen[id.Value] {
				seen[id.Value] = true
				out = append(out, id.Value)
			}
		}
	}
	return out
}

// relatorCode maps an OverDrive creator role to its MARC/LoC relator code, or "" when
// the role has no controlled mapping (bibframe.go turns a code into a relators IRI).
func relatorCode(role string) string {
	switch role {
	case "Author":
		return "aut"
	case "Narrator":
		return "nrt"
	case "Illustrator":
		return "ill"
	case "Editor":
		return "edt"
	case "Translator":
		return "trl"
	default:
		return ""
	}
}

// iso639_2 maps an ISO 639-1 code (the feed's language id) to ISO 639-2/B, the code
// the BIBFRAME language node uses. Unmapped codes return "" (omitted).
func iso639_2(code string) string {
	return iso639[strings.ToLower(code)]
}

var iso639 = map[string]string{
	"en": "eng", "es": "spa", "fr": "fre", "de": "ger", "it": "ita",
	"pt": "por", "nl": "dut", "ru": "rus", "ja": "jpn", "zh": "chi",
	"ko": "kor", "ar": "ara", "sv": "swe", "da": "dan", "no": "nor",
	"fi": "fin", "pl": "pol", "cs": "cze", "el": "gre", "he": "heb",
	"hi": "hin", "tr": "tur", "uk": "ukr", "vi": "vie",
}
