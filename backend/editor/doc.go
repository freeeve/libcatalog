package editor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"

	"github.com/freeeve/libcat/backend/profiles"
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
	// Overridden marks feed values shadowed by an lcat:overrides marker.
	Overridden bool `json:"overridden,omitempty"`
	// Annotation is the field's display-only qualifier resolved from the
	// value's structure node (e.g. a heading's bf:source label, a
	// contribution's bf:role label). Its quads stay in passthrough; ToGrain
	// ignores it.
	Annotation string `json:"annotation,omitempty"`
	// Primary marks a chained value whose structure head is typed
	// bflc:PrimaryContribution -- the author sorts before the
	// narrator. Display-only; ToGrain ignores it.
	Primary bool `json:"primary,omitempty"`
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
	WorkID string `json:"workId"`
	// ProfileID shapes the Work fields; InstanceProfileID the Instances' --
	// exposed so the editor can drive each form from the deployment's profile
	// rather than a hardcoded field list. Empty when no
	// instance profile is configured.
	ProfileID         string        `json:"profileId"`
	InstanceProfileID string        `json:"instanceProfileId,omitempty"`
	Work              ResourceDoc   `json:"work"`
	Instances         []ResourceDoc `json:"instances"`
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
	// A multi-feed cluster describes the same instance IRI in several
	// graphs, and ScanGrain reports it once per graph -- deduped here or
	// the doc grows empty husk entries (the first claims every quad) whose
	// duplicate ids crash the editor's keyed tab list.
	var instanceIDs []string
	seenInst := map[string]bool{}
	for _, inst := range gi.Instances {
		if inst.WorkID == workID && !seenInst[inst.InstanceID] {
			seenInst[inst.InstanceID] = true
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
	if m.InstanceProfile != nil {
		doc.InstanceProfileID = m.InstanceProfile.ID
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
			// A direct field's annotation chain resolves from each IRI
			// value's own node: subjects carry the
			// grain-written skos:prefLabel of the authority IRI, so the
			// doc shows names even when no vocab snapshot is installed.
			if len(field.Annotation) > 0 {
				for i := range values {
					if !values[i].IRI {
						continue
					}
					if note := annotationLabel(ds, rdf.NewIRI(values[i].V), field.Annotation); note != "" {
						values[i].Annotation = note
					}
				}
			}
		} else {
			// The link quads (node -> intermediates) stay unclaimed: they
			// belong to the structure, not the value, and passthrough
			// preserves them. An override marker for a chained field sits
			// on the chain head (resource, first predicate) -- when
			// present, every feed-sourced leaf value is shadowed.
			headOverridden := node.IsIRI() && overrides.Shadows(node.Value, field.Predicates[0])
			// Each hop keeps its structure head (the first-hop node, e.g.
			// the bf:Contribution): the annotation chain and the primary
			// marker hang off it, not off the deepest intermediate
			//. For 2-hop chains head and node coincide, so
			// annotation behavior there is unchanged.
			type chainHop struct{ head, node rdf.Term }
			hops := []chainHop{{head: node, node: node}}
			for hi, pred := range field.Predicates[:len(field.Predicates)-1] {
				var next []chainHop
				for _, h := range hops {
					for _, o := range objectsAll(ds, h.node, pred) {
						head := h.head
						if hi == 0 {
							head = o
						}
						next = append(next, chainHop{head: head, node: o})
					}
				}
				hops = next
			}
			leaf := field.Predicates[len(field.Predicates)-1]
			for _, h := range hops {
				vals := claimDirect(ds, claimed, h.node, leaf, overrides, headOverridden)
				if len(field.Annotation) > 0 {
					if note := annotationLabel(ds, h.head, field.Annotation); note != "" {
						for i := range vals {
							vals[i].Annotation = note
						}
					}
				}
				if isPrimaryContribution(ds, h.head) {
					for i := range vals {
						vals[i].Primary = true
					}
				}
				values = append(values, vals...)
			}
		}
		if len(values) > 0 {
			sort.Slice(values, func(i, j int) bool {
				if values[i].Primary != values[j].Primary {
					return values[i].Primary
				}
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
		// A structured object stays passthrough. That means blank nodes
		// and, equally, the grain-local IRIs a clone or the editor mints
		// for them: an uncontrolled bf:subject heading skolemized to
		// #<id>n<k> is the same node it was as _:b0, and rendering it as a
		// controlled-term chip would put a raw fragment IRI in front of the
		// cataloger. GrainLocalIRI is the seam the projector and the ingest
		// summarizer already read this way.
		if !q.O.IsLiteral() && !(q.O.IsIRI() && !bibframe.GrainLocalIRI(q.O.Value)) {
			continue
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
// sorted and joined. When the leaf literals carry language tags, one
// language is chosen first -- English, then untagged, then the
// lexicographically first tag (the PickLabel order) -- so a
// multilingual vocabulary annotates as one label, not a concatenation of
// translations. Nothing is claimed -- the annotation's quads
// stay in passthrough.
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
	byLang := map[string]map[string]bool{}
	for _, t := range terms {
		for _, o := range objectsAll(ds, t, leaf) {
			if o.IsLiteral() && o.Value != "" {
				set := byLang[o.Lang]
				if set == nil {
					set = map[string]bool{}
					byLang[o.Lang] = set
				}
				set[o.Value] = true
			}
		}
	}
	if len(byLang) == 0 {
		return ""
	}
	lang := ""
	if _, ok := byLang["en"]; ok {
		lang = "en"
	} else if _, ok := byLang[""]; !ok {
		langs := make([]string, 0, len(byLang))
		for l := range byLang {
			langs = append(langs, l)
		}
		sort.Strings(langs)
		lang = langs[0]
	}
	labels := make([]string, 0, len(byLang[lang]))
	for l := range byLang[lang] {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	return strings.Join(labels, ", ")
}

// isPrimaryContribution reports whether a chained value's structure head is
// typed bflc:PrimaryContribution, so the doc can sort the
// primary agent (the author) before added contributions (the narrator).
func isPrimaryContribution(ds *rdf.Dataset, head rdf.Term) bool {
	const primaryType = "http://id.loc.gov/ontologies/bflc/PrimaryContribution"
	for _, o := range objectsAll(ds, head, "http://www.w3.org/1999/02/22-rdf-syntax-ns#type") {
		if o.IsIRI() && o.Value == primaryType {
			return true
		}
	}
	return false
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
