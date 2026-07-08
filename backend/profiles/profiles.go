// Package profiles defines editing profiles -- the JSON documents that
// replace MARC frameworks (Koha's tag/subfield configuration) for the
// BIBFRAME-native editor. A profile declares which fields a form shows,
// their cardinality, datatype, value source, and defaults; the framework
// ships conservative defaults and a deployment overrides or adds its own as
// git-reviewed JSON. Validate is the "framework test": every profile is
// checked at load, so a bad profile fails boot, not a cataloger's save.
package profiles

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

//go:embed defaults/*.json
var defaults embed.FS

// ResourceType names what a profile edits.
type ResourceType string

const (
	ResourceWork      ResourceType = "work"
	ResourceInstance  ResourceType = "instance"
	ResourceItem      ResourceType = "item"
	ResourceAuthority ResourceType = "authority"
)

// ValueKind names how a field's values are entered and validated.
type ValueKind string

const (
	// KindLiteral is a plain string.
	KindLiteral ValueKind = "literal"
	// KindLangLiteral is a language-tagged string.
	KindLangLiteral ValueKind = "langLiteral"
	// KindDate is an EDTF/ISO date string.
	KindDate ValueKind = "date"
	// KindEnum restricts values to the field's Options.
	KindEnum ValueKind = "enum"
	// KindVocab is a controlled term picked from the vocabulary index
	// (Ref = scheme); the stored value is the term URI.
	KindVocab ValueKind = "vocab"
	// KindAuthority is a local-authority link (Ref = authority profile id).
	KindAuthority ValueKind = "authority"
	// KindEntity is an IRI reference to another catalog entity.
	KindEntity ValueKind = "entity"
)

var validKinds = []ValueKind{KindLiteral, KindLangLiteral, KindDate, KindEnum, KindVocab, KindAuthority, KindEntity}

// ValueSource declares a field's entry mechanism.
type ValueSource struct {
	Kind ValueKind `json:"kind"`
	// Ref points at the kind's backing set (vocab scheme, authority profile).
	Ref string `json:"ref,omitempty"`
	// Options inlines the allowed values for enum fields.
	Options []string `json:"options,omitempty"`
}

// Field is one editable property.
type Field struct {
	// Path is the field's key in the WorkDoc (unique per profile).
	Path string `json:"path"`
	// Predicates is the property chain from the resource node to the value:
	// one IRI for a direct property, two or three for a value hanging off
	// intermediate nodes (e.g. bf:title -> bf:mainTitle, or
	// bf:contribution -> bf:agent -> rdfs:label).
	Predicates []string `json:"predicates"`
	Label      string   `json:"label"`
	Help       string   `json:"help,omitempty"`
	// Min/Max bound cardinality; Max 0 = unbounded.
	Min         int         `json:"min,omitempty"`
	Max         int         `json:"max,omitempty"`
	ValueSource ValueSource `json:"valueSource"`
	// ReadOnly renders the field's values (with provenance) but rejects
	// ops against it -- for values living inside typed blank structures
	// the op layer cannot rebuild yet (e.g. contributions).
	ReadOnly bool `json:"readOnly,omitempty"`
	// Annotation is a predicate chain resolved from each value's structure
	// node into a display-only qualifier (e.g. a heading's bf:source label,
	// MARC's $2). Chained fields only; the annotation's quads stay in
	// passthrough, so it never affects the doc round trip.
	Annotation []string `json:"annotation,omitempty"`
	Default    string   `json:"default,omitempty"`
	Hidden     bool     `json:"hidden,omitempty"`
	// MarcHint names the roughly-equivalent MARC field for copy catalogers.
	MarcHint string `json:"marcHint,omitempty"`
}

// Profile is one editing profile.
type Profile struct {
	ID           string       `json:"id"`
	Label        string       `json:"label"`
	ResourceType ResourceType `json:"resourceType"`
	Fields       []Field      `json:"fields"`
}

// knownPredicatePrefixes anchor profile predicates to vocabularies the
// pipeline actually understands -- the same guard rationale as the editorial
// predicate allowlist.
var knownPredicatePrefixes = []string{
	"http://id.loc.gov/ontologies/bibframe/",
	"http://id.loc.gov/ontologies/bflc/",
	"https://github.com/freeeve/libcat/ns#",
	"http://www.w3.org/2004/02/skos/core#",
	"http://www.w3.org/2000/01/rdf-schema#",
	"http://www.w3.org/1999/02/22-rdf-syntax-ns#",
}

// Validate checks one profile's internal consistency.
func (p *Profile) Validate() error {
	if p.ID == "" || p.Label == "" {
		return fmt.Errorf("profiles: profile needs id and label")
	}
	switch p.ResourceType {
	case ResourceWork, ResourceInstance, ResourceItem, ResourceAuthority:
	default:
		return fmt.Errorf("profiles: %s: unknown resourceType %q", p.ID, p.ResourceType)
	}
	if len(p.Fields) == 0 {
		return fmt.Errorf("profiles: %s: no fields", p.ID)
	}
	seen := map[string]bool{}
	for _, f := range p.Fields {
		if f.Path == "" {
			return fmt.Errorf("profiles: %s: field with empty path", p.ID)
		}
		if seen[f.Path] {
			return fmt.Errorf("profiles: %s: duplicate field path %q", p.ID, f.Path)
		}
		seen[f.Path] = true
		if n := len(f.Predicates); n < 1 || n > 3 {
			return fmt.Errorf("profiles: %s/%s: predicate chains must be 1 to 3 long", p.ID, f.Path)
		}
		if len(f.Predicates) == 3 && !f.ReadOnly {
			return fmt.Errorf("profiles: %s/%s: 3-predicate chains must be readOnly (ops cannot build nested structures)", p.ID, f.Path)
		}
		for _, pred := range f.Predicates {
			if !knownPredicate(pred) {
				return fmt.Errorf("profiles: %s/%s: predicate %s outside known vocabularies", p.ID, f.Path, pred)
			}
		}
		if len(f.Annotation) > 0 {
			// Chained fields resolve the annotation from the structure
			// node; a direct field resolves it from each IRI value's own
			// node (tasks/140), so it needs an entity-valued kind.
			if len(f.Predicates) < 2 && f.ValueSource.Kind != KindVocab && f.ValueSource.Kind != KindEntity {
				return fmt.Errorf("profiles: %s/%s: annotation on a direct field requires entity or vocab values (the annotation resolves from the value node)", p.ID, f.Path)
			}
			if len(f.Annotation) > 2 {
				return fmt.Errorf("profiles: %s/%s: annotation chains must be 1 or 2 long", p.ID, f.Path)
			}
			for _, pred := range f.Annotation {
				if !knownPredicate(pred) {
					return fmt.Errorf("profiles: %s/%s: annotation predicate %s outside known vocabularies", p.ID, f.Path, pred)
				}
			}
		}
		if !slices.Contains(validKinds, f.ValueSource.Kind) {
			return fmt.Errorf("profiles: %s/%s: unknown value kind %q", p.ID, f.Path, f.ValueSource.Kind)
		}
		if f.ValueSource.Kind == KindEnum && len(f.ValueSource.Options) == 0 {
			return fmt.Errorf("profiles: %s/%s: enum without options", p.ID, f.Path)
		}
		if f.ValueSource.Kind == KindVocab && f.ValueSource.Ref == "" {
			return fmt.Errorf("profiles: %s/%s: vocab without a scheme ref", p.ID, f.Path)
		}
		if f.Max != 0 && f.Min > f.Max {
			return fmt.Errorf("profiles: %s/%s: min %d > max %d", p.ID, f.Path, f.Min, f.Max)
		}
		if f.Default != "" {
			if err := checkValue(f, f.Default); err != nil {
				return fmt.Errorf("profiles: %s/%s: default: %w", p.ID, f.Path, err)
			}
		}
	}
	return nil
}

// checkValue type-checks one value against a field definition.
func checkValue(f Field, v string) error {
	switch f.ValueSource.Kind {
	case KindDate:
		for _, layout := range []string{"2006", "2006-01", "2006-01-02"} {
			if _, err := time.Parse(layout, v); err == nil {
				return nil
			}
		}
		return fmt.Errorf("%q is not a date (YYYY[-MM[-DD]])", v)
	case KindEnum:
		if !slices.Contains(f.ValueSource.Options, v) {
			return fmt.Errorf("%q not in options %v", v, f.ValueSource.Options)
		}
	case KindVocab, KindAuthority, KindEntity:
		if !strings.Contains(v, ":") {
			return fmt.Errorf("%q is not an IRI", v)
		}
	}
	return nil
}

func knownPredicate(pred string) bool {
	for _, prefix := range knownPredicatePrefixes {
		if strings.HasPrefix(pred, prefix) {
			return true
		}
	}
	return false
}

// Set is a loaded, validated profile collection keyed by id.
type Set map[string]*Profile

// LoadDefaults returns the framework's shipped profiles.
func LoadDefaults() (Set, error) {
	set := Set{}
	err := fs.WalkDir(defaults, "defaults", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := defaults.ReadFile(path)
		if err != nil {
			return err
		}
		return set.add(path, data)
	})
	return set, err
}

// LoadDir loads and validates a deployment's profile overrides on top of the
// defaults (same id replaces).
func LoadDir(base Set, dir string) (Set, error) {
	set := Set{}
	maps.Copy(set, base)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if err := set.add(entry.Name(), data); err != nil {
			return nil, err
		}
	}
	return set, nil
}

// Parse unmarshals and validates one profile document. It is the single
// validator shared by the embedded loader, LoadDir, and the runtime editing
// service, so every entry point applies the same "framework test".
func Parse(data []byte) (*Profile, error) {
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (s Set) add(source string, data []byte) error {
	p, err := Parse(data)
	if err != nil {
		return fmt.Errorf("profiles: %s: %w", source, err)
	}
	s[p.ID] = p
	return nil
}

// ForResource returns the set's profiles for one resource type, id-sorted.
func (s Set) ForResource(rt ResourceType) []*Profile {
	var out []*Profile
	for _, p := range s {
		if p.ResourceType == rt {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b *Profile) int { return strings.Compare(a.ID, b.ID) })
	return out
}
