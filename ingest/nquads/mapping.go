package nquads

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
)

// Mapping declares how a deployment's N-Quads export maps onto ingest records
// -- the whole provider config, written as a TOML file so adopting a new
// dcterms-shaped export means editing a mapping, not writing Go.
// Only subjects under WorkPrefix are read as work records; skos prefLabel and
// broader statements on any other subject are harvested as authority-term
// descriptions for the works' controlled subjects (and their ancestor
// chains).
type Mapping struct {
	// WorkPrefix is the IRI prefix of work subjects; the remainder is the
	// export's work id.
	WorkPrefix string `toml:"work-prefix"`
	// IDScheme namespaces the durable provider id ("<scheme>:<workid>")
	// minted for each work. Defaults to the feed name; keep it stable across
	// exports or every work re-mints.
	IDScheme string `toml:"id-scheme"`
	// Class is the BIBFRAME work class (default "Text").
	Class string `toml:"class"`
	// DefaultLanguage is the ISO 639-2/B code used when the export carries no
	// (mappable) language (default "eng").
	DefaultLanguage string `toml:"default-language"`
	// IdentityLanguage, when set, fixes the language in every record's
	// resolution identity (identity.WorkKey folds author|title|LANG) while
	// Work.Languages keeps the export's real values. Set it when replacing a
	// provider that keyed everything under one language, so freshly-minted
	// WorkIDs match the old provider's; leave empty to key by
	// the record's language.
	IdentityLanguage string `toml:"identity-language"`
	// IDOrder orders records for deterministic ingest: "lexical" (default) or
	// "numeric" (decimal ids without padding, shorter ids first).
	IDOrder string `toml:"id-order"`
	// Predicates maps record fields to the predicate IRIs that carry them.
	// Work-level fields: title, subtitle, creator, contributor, summary,
	// subject, tag, keyword, classification, source, language, group.
	// Bucket-level fields: identifier, publisher, issued, format.
	// Term-description fields (non-work subjects): prefLabel, broader.
	// A field may list several IRIs.
	Predicates map[string]StringList `toml:"predicates"`
	// Identifiers maps object URN prefixes to identifier rules. The legacy
	// string form keeps its meaning: scheme "isbn" clusters cross-feed, any
	// other scheme becomes a durable source-tagged id key
	// ("<scheme>:<value>"). The table form ({class, source, key}) covers
	// non-key identifiers -- display ISBNs, ASINs, availability ids -- that
	// must ride the Instance without becoming resolution keys.
	Identifiers map[string]IdentifierRule `toml:"identifiers"`
	// Languages maps the export's language codes to ISO 639-2/B. An unmapped
	// three-letter code passes through; anything else falls back to
	// DefaultLanguage.
	Languages map[string]string `toml:"languages"`
	// KeywordSource is the bf:source on "keyword" topic subjects (e.g.
	// "overdrive"); "tag" topics always carry none.
	KeywordSource string `toml:"keyword-source"`
	// ExtrasPrefix, when set, harvests every work statement whose PREDICATE
	// starts with it as a display extra: key = the predicate remainder,
	// value = the literal, verbatim. First statement wins a key.
	// The [sources] mechanism owns its extra key; on a collision it wins.
	ExtrasPrefix string `toml:"extras-prefix"`
	// Classifications describes the "classification" field's objects.
	Classifications ClassificationMapping `toml:"classifications"`
	// Sources describes provenance attestation objects.
	Sources SourcesMapping `toml:"sources"`
}

// ClassificationMapping maps "classification" predicate objects -- coded IRIs
// like urn:bisac:FIC000000 -- to BIBFRAME classifications: Value is the IRI
// minus Prefix, Label the IRI's harvested skos:prefLabel, Source the scheme
// code.
type ClassificationMapping struct {
	// Prefix is the object-IRI prefix stripped to form the code value.
	Prefix string `toml:"prefix"`
	// Source is the bf:source scheme code (e.g. "bisacsh").
	Source string `toml:"source"`
}

// IdentifierRule is one identifier prefix's handling. Exactly one of the two
// forms applies: the legacy string form sets Scheme (always a resolution
// key); the table form sets Class/Source/Key.
type IdentifierRule struct {
	// Scheme is the legacy string form: "isbn" makes the value a cross-feed
	// ISBN merge key; any other scheme emits "<scheme>:<value>" as both a
	// SchemeID resolution key and a source-tagged Instance identifier.
	Scheme string
	// Class is the emitted identifier class: "Isbn" or "Identifier"
	// (default). Table form only.
	Class string
	// Source is the emitted bf:source tag; also the key namespace when Key
	// is true. Table form only.
	Source string
	// Key makes the identifier a resolution key: an "Isbn"-class value
	// merges cross-feed, any other joins as "<source>:<value>". Table form
	// only (the legacy string form is always a key).
	Key bool
}

// UnmarshalTOML implements toml.Unmarshaler for the string-or-table union.
func (r *IdentifierRule) UnmarshalTOML(v any) error {
	switch val := v.(type) {
	case string:
		r.Scheme = val
		r.Key = true
	case map[string]any:
		for k, e := range val {
			switch k {
			case "class":
				s, ok := e.(string)
				if !ok || (s != "Isbn" && s != "Identifier") {
					return fmt.Errorf("nquads mapping: identifier class %v (want Isbn or Identifier)", e)
				}
				r.Class = s
			case "source":
				s, ok := e.(string)
				if !ok {
					return fmt.Errorf("nquads mapping: identifier source %v is not a string", e)
				}
				r.Source = s
			case "key":
				b, ok := e.(bool)
				if !ok {
					return fmt.Errorf("nquads mapping: identifier key %v is not a bool", e)
				}
				r.Key = b
			default:
				return fmt.Errorf("nquads mapping: unknown identifier rule key %q (want class, source, key)", k)
			}
		}
	default:
		return fmt.Errorf("nquads mapping: identifier value %v is neither scheme string nor rule table", v)
	}
	return nil
}

// StringList decodes from either a single TOML string or an array of strings,
// so the common one-IRI field stays one line.
type StringList []string

// UnmarshalTOML implements toml.Unmarshaler.
func (s *StringList) UnmarshalTOML(v any) error {
	switch val := v.(type) {
	case string:
		*s = StringList{val}
	case []any:
		for _, e := range val {
			str, ok := e.(string)
			if !ok {
				return fmt.Errorf("nquads mapping: predicate list holds a non-string %v", e)
			}
			*s = append(*s, str)
		}
	default:
		return fmt.Errorf("nquads mapping: predicate value %v is neither string nor list", v)
	}
	return nil
}

// SourcesMapping describes the source-attestation objects a "source" field
// predicate points at.
type SourcesMapping struct {
	// Prefix is stripped from the source IRI to form the source slug.
	Prefix string `toml:"prefix"`
	// ExtraKey is the work extra the joined slugs land under (default
	// "sources" -- the key the public-provenance allowlist governs).
	ExtraKey string `toml:"extra-key"`
	// Tentative lists source IRIs that do not confer confidence: a work
	// attested only by these is marked with the "tentative" extra and can be
	// dropped wholesale via Params["tentative"]="drop".
	Tentative []string `toml:"tentative"`
}

// mappedFields are the record fields Predicates may target (the
// extensions marked): work-level, bucket-level, and the
// term-description fields harvested from non-work subjects.
var mappedFields = map[string]bool{
	"title": true, "creator": true, "identifier": true, "subject": true,
	"source": true, "language": true, "prefLabel": true,
	//
	"subtitle": true, "summary": true, "contributor": true,
	"publisher": true, "issued": true, "format": true,
	"tag": true, "keyword": true, "classification": true,
	"group": true, "broader": true,
}

// LoadMapping reads and validates a mapping TOML file.
func LoadMapping(path string) (*Mapping, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("nquads mapping: %w", err)
	}
	var m Mapping
	if err := toml.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("nquads mapping %s: %w", path, err)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("nquads mapping %s: %w", path, err)
	}
	m.applyDefaults()
	return &m, nil
}

func (m *Mapping) validate() error {
	if m.WorkPrefix == "" {
		return fmt.Errorf("work-prefix is required")
	}
	if len(m.Predicates) == 0 {
		return fmt.Errorf("at least one [predicates] entry is required")
	}
	for field := range m.Predicates {
		if !mappedFields[field] {
			return fmt.Errorf("unknown predicate field %q", field)
		}
	}
	switch m.IDOrder {
	case "", "lexical", "numeric":
	default:
		return fmt.Errorf("id-order %q (want lexical or numeric)", m.IDOrder)
	}
	for prefix, rule := range m.Identifiers {
		if rule.Scheme == "" && rule.Key && rule.Class != "Isbn" && rule.Source == "" {
			return fmt.Errorf("identifier %q: key = true needs a source (the key namespace) or class Isbn", prefix)
		}
	}
	if len(m.Predicates["classification"]) > 0 && m.Classifications.Prefix == "" {
		return fmt.Errorf("a classification predicate needs [classifications] prefix")
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
	if m.IDOrder == "" {
		m.IDOrder = "lexical"
	}
	if m.Sources.ExtraKey == "" {
		m.Sources.ExtraKey = "sources"
	}
}

// fieldFor inverts Predicates into the predicate-IRI -> field lookup the scan
// loop uses.
func (m *Mapping) fieldFor() map[string]string {
	out := map[string]string{}
	for field, iris := range m.Predicates {
		for _, iri := range iris {
			out[iri] = field
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
