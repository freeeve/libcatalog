package csvmap

import (
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Mapping declares how a CSV export's columns map onto ingest records -- the
// whole provider config, written as a TOML file so sideloading a spreadsheet
// means editing a mapping, not writing Go.
type Mapping struct {
	// IDScheme namespaces the durable provider id ("<scheme>:<rowid>") minted
	// when an id column is mapped. Defaults to the feed name; keep it stable
	// across exports or every work re-mints.
	IDScheme string `toml:"id-scheme"`
	// Class is the BIBFRAME work class (default "Text").
	Class string `toml:"class"`
	// DefaultLanguage is the ISO 639-2/B code used when a row carries no
	// (mappable) language (default "eng").
	DefaultLanguage string `toml:"default-language"`
	// Delimiter is the field separator, e.g. "\t" for TSV (default ",").
	Delimiter string `toml:"delimiter"`
	// MultiSeparator splits multi-valued cells -- creators, isbns, subjects
	// (default ";").
	MultiSeparator string `toml:"multi-separator"`
	// Columns maps record fields to header names. Fields: id, title,
	// subtitle, summary, creator, isbn, subject, language. title is required;
	// subjects are uncontrolled labels (feed tags), not authority URIs.
	Columns map[string]string `toml:"columns"`
	// Extras maps work extra keys to header names -- adopter display fields
	// carried through to catalog.json's extra object (e.g. cover, rating).
	Extras map[string]string `toml:"extras"`
	// Languages maps the export's language codes to ISO 639-2/B. An unmapped
	// three-letter code passes through; anything else falls back to
	// DefaultLanguage.
	Languages map[string]string `toml:"languages"`
}

// mappedColumns are the record fields Columns may target.
var mappedColumns = map[string]bool{
	"id": true, "title": true, "subtitle": true, "summary": true,
	"creator": true, "isbn": true, "subject": true, "language": true,
}

// LoadMapping reads and validates a mapping TOML file.
func LoadMapping(path string) (*Mapping, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("csv mapping: %w", err)
	}
	var m Mapping
	if err := toml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("csv mapping %s: %w", path, err)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("csv mapping %s: %w", path, err)
	}
	m.applyDefaults()
	return &m, nil
}

func (m *Mapping) validate() error {
	if m.Columns["title"] == "" {
		return fmt.Errorf("[columns] title is required")
	}
	for field := range m.Columns {
		if !mappedColumns[field] {
			return fmt.Errorf("unknown column field %q (want id, title, subtitle, summary, creator, isbn, subject, language)", field)
		}
	}
	if len(m.Delimiter) > 1 {
		return fmt.Errorf("delimiter %q must be a single character", m.Delimiter)
	}
	return nil
}

func (m *Mapping) applyDefaults() {
	if m.Class == "" {
		m.Class = "Text"
	}
	if m.DefaultLanguage == "" {
		m.DefaultLanguage = "eng"
	}
	if m.MultiSeparator == "" {
		m.MultiSeparator = ";"
	}
}

// delimiter returns the CSV field separator rune.
func (m *Mapping) delimiter() rune {
	if m.Delimiter == "" {
		return ','
	}
	return rune(m.Delimiter[0])
}

// checkColumns verifies every mapped header exists in the file.
func (m *Mapping) checkColumns(col map[string]int) error {
	for field, name := range m.Columns {
		if _, ok := col[name]; !ok {
			return fmt.Errorf("mapped %s column %q not in header", field, name)
		}
	}
	for key, name := range m.Extras {
		if _, ok := col[name]; !ok {
			return fmt.Errorf("mapped extra %q column %q not in header", key, name)
		}
	}
	return nil
}

// split breaks a multi-valued cell on the separator, trimming and dropping
// empties.
func (m *Mapping) split(v string) []string {
	if v == "" {
		return nil
	}
	var out []string
	for s := range strings.SplitSeq(v, m.MultiSeparator) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// language maps an export language code to ISO 639-2/B per the mapping table.
func (m *Mapping) language(code string) string {
	if v, ok := m.Languages[code]; ok {
		return v
	}
	if len(code) == 3 {
		return code
	}
	return m.DefaultLanguage
}
