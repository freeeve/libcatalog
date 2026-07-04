package editor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/identity"

	"github.com/freeeve/libcatalog/backend/profiles"
)

// FieldValue is one value of a profile field, with provenance for the
// editor's badges and the reconstruction metadata (Node) that keeps the
// grain <-> doc mapping lossless.
type FieldValue struct {
	V    string `json:"v"`
	Lang string `json:"lang,omitempty"`
	// Datatype preserves a literal's explicit datatype IRI.
	Datatype string `json:"datatype,omitempty"`
	// IRI marks an entity-valued field (V is the IRI).
	IRI bool `json:"iri,omitempty"`
	// Prov is the named graph the value came from ("feed:overdrive",
	// "editorial:", "enrichment:locsh").
	Prov string `json:"prov"`
	// Overridden marks feed values shadowed by an lcat:overrides marker
	// (tasks/042).
	Overridden bool `json:"overridden,omitempty"`
	// Annotation is the field's display-only qualifier resolved from the
	// value's structure node (e.g. a heading's bf:source label). Its quads
	// stay in passthrough; ToGrain ignores it.
	Annotation string `json:"annotation,omitempty"`
	// Node is the value's subject term in N-Quads syntax -- the resource
	// node for direct fields, the intermediate node for chained fields.
	// Reconstruction metadata; clients treat it as opaque.
	Node string `json:"node"`
}

// ResourceDoc is one resource's field view.
type ResourceDoc struct {
	ID     string                  `json:"id"`
	Fields map[string][]FieldValue `json:"fields"`
}

// WorkDoc is the typed editing document one grain materializes into: the
// Work's profile fields, its Instances' fields, and a passthrough of every
// statement no field claims -- so doc -> grain reproduces the input
// byte-for-byte when nothing is edited.
type WorkDoc struct {
	WorkID    string        `json:"workId"`
	ProfileID string        `json:"profileId"`
	Work      ResourceDoc   `json:"work"`
	Instances []ResourceDoc `json:"instances"`
	// Passthrough holds the unclaimed statements as raw N-Quads lines.
	Passthrough []string `json:"passthrough"`
}

// Mapper materializes grains through a profile pair.
type Mapper struct {
	// WorkProfile shapes the Work fields; InstanceProfile the Instances'.
	WorkProfile     *profiles.Profile
	InstanceProfile *profiles.Profile
}

// ToDoc decomposes a grain into the typed document for one of its Works.
// Every quad is either claimed by exactly one profile field (rendered as a
// FieldValue) or preserved verbatim in Passthrough. Feed values whose
// (subject, predicate) carries an editorial lcat:overrides marker come back
// flagged Overridden -- shadowed in projection, shown to the editor for the
// hover-reveal / revert affordance.
func (m *Mapper) ToDoc(grainNQ []byte, workID string) (*WorkDoc, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	overrides := bibframe.ScanOverrides(ds)
	gi, err := identity.ScanGrain(grainNQ)
	if err != nil {
		return nil, err
	}
	var instanceIDs []string
	for _, inst := range gi.Instances {
		if inst.WorkID == workID {
			instanceIDs = append(instanceIDs, inst.InstanceID)
		}
	}
	sort.Strings(instanceIDs)

	claimed := make([]bool, len(ds.Quads))
	doc := &WorkDoc{
		WorkID:    workID,
		ProfileID: m.WorkProfile.ID,
		Work:      ResourceDoc{ID: workID, Fields: map[string][]FieldValue{}},
	}
	claimFields(ds, claimed, rdf.NewIRI(bibframe.WorkIRI(workID)), m.WorkProfile, doc.Work.Fields, overrides)
	for _, instID := range instanceIDs {
		inst := ResourceDoc{ID: instID, Fields: map[string][]FieldValue{}}
		if m.InstanceProfile != nil {
			claimFields(ds, claimed, rdf.NewIRI(bibframe.InstanceIRI(instID)), m.InstanceProfile, inst.Fields, overrides)
		}
		doc.Instances = append(doc.Instances, inst)
	}

	for i, q := range ds.Quads {
		if claimed[i] {
			continue
		}
		doc.Passthrough = append(doc.Passthrough, renderQuad(q))
	}
	sort.Strings(doc.Passthrough)
	return doc, nil
}

// renderQuad writes one quad as an N-Quads line, preserving blank-node
// labels verbatim (the stock Encoder renames them, which would break the
// linkage between passthrough structure and claimed values' Node terms).
func renderQuad(q rdf.Quad) string {
	var b strings.Builder
	b.WriteString(termSyntax(q.S))
	b.WriteByte(' ')
	b.WriteString(termSyntax(q.P))
	b.WriteByte(' ')
	b.WriteString(objectSyntax(q.O))
	b.WriteByte(' ')
	b.WriteString(termSyntax(q.G))
	b.WriteString(" .")
	return b.String()
}

// objectSyntax renders an object term (IRI, blank, or literal with N-Quads
// escaping).
func objectSyntax(t rdf.Term) string {
	if !t.IsLiteral() {
		return termSyntax(t)
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range t.Value {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	if t.Lang != "" {
		b.WriteByte('@')
		b.WriteString(t.Lang)
	} else if t.Datatype != "" && t.Datatype != "http://www.w3.org/2001/XMLSchema#string" {
		b.WriteString("^^<")
		b.WriteString(t.Datatype)
		b.WriteByte('>')
	}
	return b.String()
}

// claimFields walks one profile's fields against one resource node, claiming
// matching quads into FieldValues.
func claimFields(ds *rdf.Dataset, claimed []bool, node rdf.Term, profile *profiles.Profile, out map[string][]FieldValue, overrides bibframe.Overrides) {
	for _, field := range profile.Fields {
		var values []FieldValue
		if len(field.Predicates) == 1 {
			values = claimDirect(ds, claimed, node, field.Predicates[0], overrides, false)
		} else {
			// The link quads (node -> intermediates) stay unclaimed: they
			// belong to the structure, not the value, and passthrough
			// preserves them. An override marker for a chained field sits
			// on the chain head (resource, first predicate) -- when
			// present, every feed-sourced leaf value is shadowed.
			headOverridden := node.IsIRI() && overrides.Shadows(node.Value, field.Predicates[0])
			mids := []rdf.Term{node}
			for _, pred := range field.Predicates[:len(field.Predicates)-1] {
				var next []rdf.Term
				for _, mid := range mids {
					next = append(next, objectsAll(ds, mid, pred)...)
				}
				mids = next
			}
			leaf := field.Predicates[len(field.Predicates)-1]
			for _, mid := range mids {
				vals := claimDirect(ds, claimed, mid, leaf, overrides, headOverridden)
				if len(field.Annotation) > 0 {
					if note := annotationLabel(ds, mid, field.Annotation); note != "" {
						for i := range vals {
							vals[i].Annotation = note
						}
					}
				}
				values = append(values, vals...)
			}
		}
		if len(values) > 0 {
			sort.Slice(values, func(i, j int) bool {
				if values[i].V != values[j].V {
					return values[i].V < values[j].V
				}
				return values[i].Prov < values[j].Prov
			})
			out[field.Path] = values
		}
	}
}

// claimDirect claims every (subject, predicate, *) quad across all graphs.
// forceOverridden shadows feed values whose chain head carries the field's
// override marker.
func claimDirect(ds *rdf.Dataset, claimed []bool, subject rdf.Term, predicate string, overrides bibframe.Overrides, forceOverridden bool) []FieldValue {
	var out []FieldValue
	for i, q := range ds.Quads {
		if claimed[i] || q.S != subject || q.P.Value != predicate {
			continue
		}
		if !q.O.IsLiteral() && !q.O.IsIRI() {
			continue // structured object (blank node) stays passthrough
		}
		claimed[i] = true
		out = append(out, FieldValue{
			V:        q.O.Value,
			Lang:     q.O.Lang,
			Datatype: q.O.Datatype,
			IRI:      q.O.IsIRI(),
			Prov:     q.G.Value,
			Overridden: strings.HasPrefix(q.G.Value, "feed:") &&
				(forceOverridden || (subject.IsIRI() && overrides.Shadows(subject.Value, predicate))),
			Node: termSyntax(subject),
		})
	}
	return out
}

// annotationLabel resolves a field's display annotation from one value's
// structure node: the distinct literals at the end of the annotation chain,
// sorted and joined. Nothing is claimed -- the annotation's quads stay in
// passthrough.
func annotationLabel(ds *rdf.Dataset, node rdf.Term, chain []string) string {
	terms := []rdf.Term{node}
	for _, pred := range chain[:len(chain)-1] {
		var next []rdf.Term
		for _, t := range terms {
			next = append(next, objectsAll(ds, t, pred)...)
		}
		terms = next
	}
	leaf := chain[len(chain)-1]
	seen := map[string]bool{}
	var labels []string
	for _, t := range terms {
		for _, o := range objectsAll(ds, t, leaf) {
			if o.IsLiteral() && o.Value != "" && !seen[o.Value] {
				seen[o.Value] = true
				labels = append(labels, o.Value)
			}
		}
	}
	sort.Strings(labels)
	return strings.Join(labels, ", ")
}

// objectsAll returns the objects of (subject, predicate) across every graph.
func objectsAll(ds *rdf.Dataset, subject rdf.Term, predicate string) []rdf.Term {
	var out []rdf.Term
	seen := map[rdf.Term]bool{}
	for _, q := range ds.Quads {
		if q.S == subject && q.P.Value == predicate && !seen[q.O] {
			seen[q.O] = true
			out = append(out, q.O)
		}
	}
	return out
}

// ToGrain reassembles a document into canonical grain bytes: passthrough
// statements plus each field value rendered back onto its node. An unedited
// round-trip is byte-identical to the source grain.
func (m *Mapper) ToGrain(doc *WorkDoc) ([]byte, error) {
	ds := &rdf.Dataset{}
	for _, line := range doc.Passthrough {
		parsed, err := rdf.ParseNQuads([]byte(line + "\n"))
		if err != nil {
			return nil, fmt.Errorf("editor: bad passthrough line %q: %w", line, err)
		}
		ds.Quads = append(ds.Quads, parsed.Quads...)
	}
	if err := renderFields(ds, m.WorkProfile, doc.Work); err != nil {
		return nil, err
	}
	for _, inst := range doc.Instances {
		if m.InstanceProfile == nil {
			break
		}
		if err := renderFields(ds, m.InstanceProfile, inst); err != nil {
			return nil, err
		}
	}
	return ds.Canonical()
}

// renderFields emits each field value as a quad on its recorded node.
func renderFields(ds *rdf.Dataset, profile *profiles.Profile, res ResourceDoc) error {
	byPath := map[string]profiles.Field{}
	for _, f := range profile.Fields {
		byPath[f.Path] = f
	}
	for path, values := range res.Fields {
		field, ok := byPath[path]
		if !ok {
			return fmt.Errorf("editor: field %q not in profile %s", path, profile.ID)
		}
		leaf := field.Predicates[len(field.Predicates)-1]
		for _, v := range values {
			subject, err := parseTermSyntax(v.Node)
			if err != nil {
				return fmt.Errorf("editor: field %q: %w", path, err)
			}
			var obj rdf.Term
			if v.IRI {
				obj = rdf.NewIRI(v.V)
			} else {
				obj = rdf.NewLiteral(v.V, v.Lang, v.Datatype)
			}
			ds.Add(subject, rdf.NewIRI(leaf), obj, rdf.NewIRI(v.Prov))
		}
	}
	return nil
}

// termSyntax renders a subject term in N-Quads syntax.
func termSyntax(t rdf.Term) string {
	if t.IsBlank() {
		return "_:" + t.Value
	}
	return "<" + t.Value + ">"
}

// parseTermSyntax inverts termSyntax.
func parseTermSyntax(s string) (rdf.Term, error) {
	switch {
	case strings.HasPrefix(s, "_:"):
		return rdf.NewBlank(strings.TrimPrefix(s, "_:")), nil
	case strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">"):
		return rdf.NewIRI(strings.Trim(s, "<>")), nil
	}
	return rdf.Term{}, fmt.Errorf("bad node syntax %q", s)
}
