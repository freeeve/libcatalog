package editor

import (
	"fmt"
	"slices"
	"strings"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcatalog/bibframe"

	"github.com/freeeve/libcatalog/backend/profiles"
)

// OpValue is one value in an operation.
type OpValue struct {
	V    string `json:"v"`
	Lang string `json:"lang,omitempty"`
	IRI  bool   `json:"iri,omitempty"`
}

// Op is one field-level edit. The SPA emits ordered op lists; the same shape
// backs drafts, macros, and batch templates (the plan's
// everything-is-an-operation-list rule).
type Op struct {
	// Resource is "work" or an Instance id.
	Resource string `json:"resource"`
	// Path names the profile field.
	Path string `json:"path"`
	// Action: "add" one value, "remove" one matching value, "set" the whole
	// value set, "clear" every value.
	Action string    `json:"action"`
	Value  *OpValue  `json:"value,omitempty"`  // add / remove
	Values []OpValue `json:"values,omitempty"` // set
}

// ApplyOps edits a grain through the profile mapper, translating field
// operations into the editorial write shapes of ARCHITECTURE §5:
//
//   - Adding a value asserts it editorially (direct fields on the resource
//     node; chained fields on a deterministic skolem IRI, since editorial
//     statements cannot reference the feed's blank structure nodes).
//   - Removing an editorial value retracts its quad.
//   - Any edit that suppresses feed values (remove/set/clear touching them)
//     claims the field with an lcat:overrides marker and re-asserts the
//     surviving values editorially -- the tasks/042 semantics, so the feed
//     stays untouched and revert is always possible.
//
// Returns the re-canonicalized grain.
func ApplyOps(m *Mapper, grainNQ []byte, workID string, ops []Op) ([]byte, error) {
	doc, err := m.ToDoc(grainNQ, workID)
	if err != nil {
		return nil, err
	}
	patch := bibframe.Patch{}
	for i, op := range ops {
		if err := applyOne(m, doc, workID, op, &patch); err != nil {
			return nil, fmt.Errorf("editor: op %d (%s %s.%s): %w", i, op.Action, op.Resource, op.Path, err)
		}
	}
	return bibframe.ApplyEditorialPatch(grainNQ, patch)
}

// resolveField finds the profile field and resource node for an op.
func resolveField(m *Mapper, doc *WorkDoc, workID string, op Op) (profiles.Field, string, []FieldValue, error) {
	var profile *profiles.Profile
	var nodeIRI string
	var existing []FieldValue
	if op.Resource == "" || op.Resource == "work" {
		profile = m.WorkProfile
		nodeIRI = bibframe.WorkIRI(workID)
		existing = doc.Work.Fields[op.Path]
	} else {
		profile = m.InstanceProfile
		nodeIRI = bibframe.InstanceIRI(op.Resource)
		found := false
		for _, inst := range doc.Instances {
			if inst.ID == op.Resource {
				existing = inst.Fields[op.Path]
				found = true
				break
			}
		}
		if !found {
			return profiles.Field{}, "", nil, fmt.Errorf("unknown instance %q", op.Resource)
		}
		if profile == nil {
			return profiles.Field{}, "", nil, fmt.Errorf("no instance profile configured")
		}
	}
	for _, f := range profile.Fields {
		if f.Path == op.Path {
			return f, nodeIRI, existing, nil
		}
	}
	return profiles.Field{}, "", nil, fmt.Errorf("field %q not in profile %s", op.Path, profile.ID)
}

func applyOne(m *Mapper, doc *WorkDoc, workID string, op Op, patch *bibframe.Patch) error {
	field, nodeIRI, existing, err := resolveField(m, doc, workID, op)
	if err != nil {
		return err
	}
	switch op.Action {
	case "add":
		if op.Value == nil {
			return fmt.Errorf("add needs a value")
		}
		if err := validateValue(field, *op.Value); err != nil {
			return err
		}
		patch.Add = append(patch.Add, valueQuads(field, nodeIRI, op.Path, []OpValue{*op.Value})...)
		return nil
	case "remove":
		if op.Value == nil {
			return fmt.Errorf("remove needs a value")
		}
		target, ok := matchValue(existing, *op.Value)
		if !ok {
			return fmt.Errorf("value not present")
		}
		if target.Prov == "editorial:" {
			patch.Remove = append(patch.Remove, existingQuad(field, target))
			return nil
		}
		// Feed-sourced: override the field, keep everything else.
		keepers := valuesExcept(existing, *op.Value)
		overrideField(field, nodeIRI, op.Path, existing, keepers, patch)
		return nil
	case "set":
		for _, v := range op.Values {
			if err := validateValue(field, v); err != nil {
				return err
			}
		}
		if field.Max == 1 && len(op.Values) > 1 {
			return fmt.Errorf("field takes at most one value")
		}
		if anyFeed(existing) {
			overrideField(field, nodeIRI, op.Path, existing, op.Values, patch)
			return nil
		}
		// Editorial-only field: retract and re-assert.
		for _, cur := range existing {
			patch.Remove = append(patch.Remove, existingQuad(field, cur))
		}
		patch.Add = append(patch.Add, valueQuads(field, nodeIRI, op.Path, op.Values)...)
		return nil
	case "clear":
		if anyFeed(existing) {
			overrideField(field, nodeIRI, op.Path, existing, nil, patch)
			return nil
		}
		for _, cur := range existing {
			patch.Remove = append(patch.Remove, existingQuad(field, cur))
		}
		return nil
	}
	return fmt.Errorf("unknown action %q", op.Action)
}

// overrideField claims the field editorially: the lcat:overrides marker on
// (resource, first predicate), retraction of any prior editorial values, and
// the final value set re-asserted.
func overrideField(field profiles.Field, nodeIRI, path string, existing []FieldValue, final []OpValue, patch *bibframe.Patch) {
	marker := bibframe.OverridePatch(nodeIRI, field.Predicates[0])
	patch.Add = append(patch.Add, marker.Add...)
	for _, cur := range existing {
		if cur.Prov == "editorial:" {
			patch.Remove = append(patch.Remove, existingQuad(field, cur))
		}
	}
	patch.Add = append(patch.Add, valueQuads(field, nodeIRI, path, final)...)
}

// valueQuads renders values as editorial statements: direct fields attach to
// the resource; chained fields attach their leaf to the field's skolem node,
// linked once from the resource.
func valueQuads(field profiles.Field, nodeIRI, path string, values []OpValue) []rdf.Quad {
	var quads []rdf.Quad
	if len(values) == 0 {
		return nil
	}
	subject := nodeIRI
	if len(field.Predicates) == 2 {
		skolem := skolemIRI(nodeIRI, path)
		quads = append(quads, rdf.Quad{
			S: rdf.NewIRI(nodeIRI), P: rdf.NewIRI(field.Predicates[0]), O: rdf.NewIRI(skolem),
		})
		subject = skolem
	}
	leaf := field.Predicates[len(field.Predicates)-1]
	for _, v := range values {
		q := rdf.Quad{S: rdf.NewIRI(subject), P: rdf.NewIRI(leaf)}
		if v.IRI {
			q.O = rdf.NewIRI(v.V)
		} else {
			q.O = rdf.NewLiteral(v.V, v.Lang, "")
		}
		quads = append(quads, q)
	}
	return quads
}

// existingQuad reconstructs the exact quad backing a claimed value (its Node
// carries the subject term).
func existingQuad(field profiles.Field, v FieldValue) rdf.Quad {
	subject, err := parseTermSyntax(v.Node)
	if err != nil {
		// Claimed values always carry valid node syntax; a mismatch means
		// a hand-built doc -- fail closed by targeting nothing removable.
		subject = rdf.NewIRI("")
	}
	leaf := field.Predicates[len(field.Predicates)-1]
	q := rdf.Quad{S: subject, P: rdf.NewIRI(leaf)}
	if v.IRI {
		q.O = rdf.NewIRI(v.V)
	} else {
		q.O = rdf.NewLiteral(v.V, v.Lang, v.Datatype)
	}
	return q
}

// skolemIRI names the editorial structure node for a chained field --
// deterministic per (resource, field), so repeated edits reuse it.
func skolemIRI(nodeIRI, path string) string {
	return nodeIRI + "-ed-" + path
}

func matchValue(existing []FieldValue, v OpValue) (FieldValue, bool) {
	for _, cur := range existing {
		if cur.V == v.V && cur.Lang == v.Lang && cur.IRI == v.IRI {
			return cur, true
		}
	}
	return FieldValue{}, false
}

func valuesExcept(existing []FieldValue, drop OpValue) []OpValue {
	var out []OpValue
	for _, cur := range existing {
		if cur.V == drop.V && cur.Lang == drop.Lang && cur.IRI == drop.IRI {
			continue
		}
		out = append(out, OpValue{V: cur.V, Lang: cur.Lang, IRI: cur.IRI})
	}
	return out
}

func anyFeed(existing []FieldValue) bool {
	return slices.ContainsFunc(existing, func(v FieldValue) bool {
		return strings.HasPrefix(v.Prov, "feed:")
	})
}

// validateValue type-checks one value against the field definition.
func validateValue(field profiles.Field, v OpValue) error {
	if v.V == "" {
		return fmt.Errorf("empty value")
	}
	kind := field.ValueSource.Kind
	wantIRI := kind == profiles.KindVocab || kind == profiles.KindAuthority || kind == profiles.KindEntity
	if wantIRI != v.IRI {
		return fmt.Errorf("field %s takes %s values", field.Path, map[bool]string{true: "IRI", false: "literal"}[wantIRI])
	}
	if kind == profiles.KindEnum && !slices.Contains(field.ValueSource.Options, v.V) {
		return fmt.Errorf("%q not in the field's options", v.V)
	}
	return nil
}
