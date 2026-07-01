// Package overdrive maps OverDrive/Libby "thunder" API records (as cached by a
// scan) into codex.Record (MARC), following docs/bibframe-field-mapping.md. It is
// the ingest half of the OverDrive provider (ARCHITECTURE §9): its output feeds
// the same bibframe.BuildCorpus path as any MARC source, so a cached collection
// becomes canonical feed:overdrive grains. The live fetch is a separate concern;
// this reads the on-disk page cache so a build needs no API call.
package overdrive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	codex "github.com/freeeve/libcodex"
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
	Type        NamedID   `json:"type"` // {id: ebook|audiobook, name}
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
// all items across the pages, in page order.
func ReadCache(dir string) ([]Item, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "page-*.json"))
	if err != nil {
		return nil, err
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

// Records maps every item to a codex.Record.
func Records(items []Item) []*codex.Record {
	recs := make([]*codex.Record, 0, len(items))
	for _, it := range items {
		recs = append(recs, it.Record())
	}
	return recs
}

// Record crosswalks one OverDrive item to a MARC record per
// docs/bibframe-field-mapping.md.
func (it Item) Record() *codex.Record {
	r := codex.NewRecord()
	r.SetLeader(leaderFor(it.Type.ID))

	if it.ID != "" {
		r.AddField(codex.NewControlField("001", it.ID))
	}
	for _, isbn := range it.ISBNs() {
		r.AddField(codex.NewDataField("020", ' ', ' ', codex.NewSubfield('a', isbn)))
	}
	if it.ID != "" {
		r.AddField(codex.NewDataField("024", '7', ' ',
			codex.NewSubfield('a', it.ID), codex.NewSubfield('2', "overdrive")))
	}
	if it.ReserveID != "" {
		r.AddField(codex.NewDataField("024", '8', ' ', codex.NewSubfield('a', it.ReserveID)))
	}
	for _, l := range it.Languages {
		if code := iso639_2(l.ID); code != "" {
			r.AddField(codex.NewDataField("041", ' ', ' ', codex.NewSubfield('a', code)))
		}
	}
	for _, b := range it.BISAC {
		if b.Code != "" {
			r.AddField(codex.NewDataField("072", ' ', '7',
				codex.NewSubfield('a', b.Code), codex.NewSubfield('2', "bisacsh")))
		}
	}

	authors, others := it.contributors()
	titleInd1 := byte('0')
	if len(authors) > 0 {
		titleInd1 = '1'
		r.AddField(codex.NewDataField("100", '1', ' ', nameSubfields(authors[0])...))
		authors = authors[1:]
	}

	title := []codex.Subfield{codex.NewSubfield('a', it.Title)}
	if it.Subtitle != "" {
		title = append(title, codex.NewSubfield('b', it.Subtitle))
	}
	r.AddField(codex.NewDataField("245", titleInd1, '0', title...))

	if it.Edition != "" {
		r.AddField(codex.NewDataField("250", ' ', ' ', codex.NewSubfield('a', it.Edition)))
	}
	if pub := it.provision(); len(pub) > 0 {
		r.AddField(codex.NewDataField("264", ' ', '1', pub...))
	}
	r.AddField(codex.NewDataField("336", ' ', ' ',
		codex.NewSubfield('a', rdaContent(it.Type.ID)), codex.NewSubfield('2', "rdacontent")))
	r.AddField(codex.NewDataField("338", ' ', ' ',
		codex.NewSubfield('a', "online resource"), codex.NewSubfield('b', "cr"), codex.NewSubfield('2', "rdacarrier")))
	if it.Series != "" {
		r.AddField(codex.NewDataField("490", '1', ' ', codex.NewSubfield('a', it.Series)))
	}
	for _, s := range it.Subjects {
		if s.Name != "" {
			r.AddField(codex.NewDataField("653", ' ', ' ', codex.NewSubfield('a', s.Name)))
		}
	}
	for _, c := range append(authors, others...) {
		r.AddField(codex.NewDataField("700", '1', ' ', nameSubfields(c)...))
	}
	return r
}

// contributor is a name field's transcribed form, relationship term, and relator.
type contributor struct {
	name, role, relator string
}

// contributors splits creators into author entries (100/700) and other roles.
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

// provision builds the 264 subfields (publisher, date).
func (it Item) provision() []codex.Subfield {
	var sf []codex.Subfield
	if it.Publisher != nil && it.Publisher.Name != "" {
		sf = append(sf, codex.NewSubfield('b', it.Publisher.Name))
	}
	if len(it.PublishDate) >= 4 {
		sf = append(sf, codex.NewSubfield('c', it.PublishDate[:4]))
	}
	return sf
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

func nameSubfields(c contributor) []codex.Subfield {
	sf := []codex.Subfield{codex.NewSubfield('a', c.name)}
	if c.role != "" {
		sf = append(sf, codex.NewSubfield('e', c.role))
	}
	if c.relator != "" {
		sf = append(sf, codex.NewSubfield('4', c.relator))
	}
	return sf
}

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

// leaderFor sets Leader/06 (type of record) by media type: text for an ebook,
// nonmusical sound recording for an audiobook. Both are monographs (/07 = m).
func leaderFor(typeID string) codex.Leader {
	b := []byte("00000nam a2200000 a 4500")
	if typeID == "audiobook" {
		b[6] = 'i'
	}
	return codex.Leader(b)
}

func rdaContent(typeID string) string {
	if typeID == "audiobook" {
		return "spoken word"
	}
	return "text"
}

// iso639_2 maps an ISO 639-1 code (the feed's language id) to ISO 639-2/B for
// MARC 041. Unmapped codes return "" (omitted).
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
