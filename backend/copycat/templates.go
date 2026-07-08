// Original cataloging (tasks/077): blank-record MARC skeletons and the
// validation gate for records that enter the catalog from the editor rather
// than an external target. A draft stages into a normal batch with source
// "original", so review, identity matching, commit, and revert are the same
// machinery every import uses.
package copycat

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/freeeve/libcat/backend/marcview"
)

//go:embed templates/*.json
var templateFS embed.FS

// Template is a blank-record skeleton for one material type: LDR and fixed
// fields prefilled, the common data fields present but empty (empty rows
// prune away at staging).
type Template struct {
	ID     string             `json:"id"`
	Label  string             `json:"label"`
	Record marcview.RecordDoc `json:"record"`
}

// LoadTemplates returns the shipped skeletons, sorted by id.
func LoadTemplates() ([]Template, error) {
	return loadTemplateFS(templateFS, "templates")
}

// LoadTemplatesDir overlays a deployment's *.json skeletons: same id
// replaces, new id adds -- the profiles override convention.
func LoadTemplatesDir(base []Template, dir string) ([]Template, error) {
	extra, err := loadTemplateFS(os.DirFS(dir), ".")
	if err != nil {
		return nil, err
	}
	byID := map[string]Template{}
	for _, t := range base {
		byID[t.ID] = t
	}
	for _, t := range extra {
		byID[t.ID] = t
	}
	out := make([]Template, 0, len(byID))
	for _, t := range byID {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func loadTemplateFS(fsys fs.FS, dir string) ([]Template, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, err
	}
	out := []Template{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := fs.ReadFile(fsys, filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		var t Template
		if err := json.Unmarshal(data, &t); err != nil {
			return nil, fmt.Errorf("template %s: %w", e.Name(), err)
		}
		if t.ID == "" || t.Label == "" {
			return nil, fmt.Errorf("template %s: id and label are required", e.Name())
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// FieldError anchors a validation failure to a MARC field ("LDR" for the
// leader).
type FieldError struct {
	Tag     string `json:"tag"`
	Message string `json:"message"`
}

// ValidateOriginal is the minimum-viability gate for editor-born records:
// a 24-character leader, one 245 with a non-empty $a, 40-character 008s,
// and well-formed fields. It judges the pruned record -- untouched skeleton
// rows are not errors.
func ValidateOriginal(doc marcview.RecordDoc) []FieldError {
	var errs []FieldError
	if len(doc.Leader) != 24 {
		errs = append(errs, FieldError{Tag: "LDR", Message: fmt.Sprintf("leader must be 24 characters (have %d)", len(doc.Leader))})
	}
	titled := false
	for _, f := range doc.Fields {
		if len(f.Tag) != 3 {
			errs = append(errs, FieldError{Tag: f.Tag, Message: "tags are three characters"})
			continue
		}
		if f.Tag == "008" && len(f.Value) != 40 {
			errs = append(errs, FieldError{Tag: "008", Message: fmt.Sprintf("008 must be 40 characters (have %d)", len(f.Value))})
		}
		if f.Tag == "245" {
			for _, sf := range f.Subfields {
				if sf.Code == "a" && strings.TrimSpace(sf.Value) != "" {
					titled = true
				}
			}
		}
	}
	if !titled {
		errs = append(errs, FieldError{Tag: "245", Message: "a title is required (245 $a)"})
	}
	return errs
}

// pruneEmptyFields drops untouched skeleton rows: empty subfields go, data
// fields with nothing left go, control fields with empty values go. The
// leader stays as typed.
func pruneEmptyFields(doc marcview.RecordDoc) marcview.RecordDoc {
	fields := make([]marcview.Field, 0, len(doc.Fields))
	for _, f := range doc.Fields {
		if len(f.Subfields) > 0 {
			kept := make([]marcview.Subfield, 0, len(f.Subfields))
			for _, sf := range f.Subfields {
				if strings.TrimSpace(sf.Value) != "" {
					kept = append(kept, sf)
				}
			}
			if len(kept) == 0 {
				continue
			}
			f.Subfields = kept
		} else if strings.TrimSpace(f.Value) == "" {
			continue
		}
		fields = append(fields, f)
	}
	doc.Fields = fields
	return doc
}

// StageOriginal prunes, validates, and stages one editor-born record as a
// source "original" batch. Field-anchored errors come back instead of a
// batch when the record fails the gate.
func (s *Service) StageOriginal(ctx context.Context, label string, doc marcview.RecordDoc, owner string) (Batch, []StagedRecord, []FieldError, error) {
	pruned := pruneEmptyFields(doc)
	if errs := ValidateOriginal(pruned); len(errs) > 0 {
		return Batch{}, nil, errs, fmt.Errorf("%w: the record fails minimum viability", ErrValidation)
	}
	if label == "" {
		label = "original: " + titleOf(pruned)
	}
	b, recs, err := s.Stage(ctx, label, "original", []marcview.RecordDoc{pruned}, owner)
	return b, recs, nil, err
}

func titleOf(doc marcview.RecordDoc) string {
	for _, f := range doc.Fields {
		if f.Tag == "245" {
			for _, sf := range f.Subfields {
				if sf.Code == "a" {
					return sf.Value
				}
			}
		}
	}
	return "(untitled)"
}
