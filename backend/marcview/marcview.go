// Package marcview is the MARC half of the dual-view editor (tasks/049): it
// materializes a grain's records as an editable field array (via the
// framework-aware decode, so overrides shadow and verbatim sidecar fields
// appear), and writes an edited array back as a *diff* -- the edited record
// re-crosswalks to BIBFRAME, the result is compared to the original decode
// per (subject, predicate) group, and only the changed groups land as
// editorial quads with the tasks/042 override semantics. Untouched fields
// are byte-for-byte no-ops; crosswalk-lossy tags round-trip through the
// lcat:marcVerbatim sidecar instead of the graph.
package marcview

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	codex "github.com/freeeve/libcodex"
	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
)

// Subfield is one (code, value) pair of a data field.
type Subfield struct {
	Code  string `json:"code"`
	Value string `json:"value"`
}

// Field is one MARC field row of the editing grid.
type Field struct {
	Tag  string `json:"tag"`
	Ind1 string `json:"ind1,omitempty"`
	Ind2 string `json:"ind2,omitempty"`
	// Value carries a control field's raw data (tag < 010).
	Value     string     `json:"value,omitempty"`
	Subfields []Subfield `json:"subfields,omitempty"`
	// Lossy carries the fidelity table's reason when the tag does not
	// survive the crosswalk -- the editor's non-blocking warning. Edits to
	// lossy tags persist via the lcat:marcVerbatim sidecar.
	Lossy string `json:"lossy,omitempty"`
}

// RecordDoc is one materialized MARC record.
type RecordDoc struct {
	// Node is the grain node the record maps to (its Instance IRI, or the
	// Work IRI for an instance-less record).
	Node   string  `json:"node"`
	Leader string  `json:"leader"`
	Fields []Field `json:"fields"`
}

// ErrValidation reports an edited record that cannot be saved.
var ErrValidation = errors.New("marcview: invalid record")

// RecordToDoc materializes one parsed MARC record as a field array -- the
// copy-cataloging search results and staged imports speak this shape too
// (tasks/050).
func RecordToDoc(rec *codex.Record) RecordDoc {
	doc := RecordDoc{Leader: rec.Leader().String()}
	for _, f := range rec.Fields() {
		doc.Fields = append(doc.Fields, fromCodexField(f))
	}
	return doc
}

// DocToRecord rebuilds the full MARC record from a field array (every field
// included, lossy or not) -- the inverse of RecordToDoc.
func DocToRecord(doc RecordDoc) (*codex.Record, error) {
	rec := codex.NewRecord()
	if doc.Leader != "" {
		rec.SetLeader(codex.Leader(doc.Leader))
	}
	for _, f := range doc.Fields {
		cf, err := toCodexField(f)
		if err != nil {
			return nil, err
		}
		rec.AddField(cf)
	}
	return rec, nil
}

// View materializes every record a grain carries.
func View(grain []byte) ([]RecordDoc, error) {
	recs, err := bibframe.DecodeGrainMARC(grain)
	if err != nil {
		return nil, err
	}
	nodes, err := bibframe.MARCRecordNodes(grain)
	if err != nil {
		return nil, err
	}
	out := make([]RecordDoc, 0, len(recs))
	for i, rec := range recs {
		doc := RecordDoc{Leader: rec.Leader().String()}
		if i < len(nodes) {
			doc.Node = nodes[i]
		}
		for _, f := range rec.Fields() {
			doc.Fields = append(doc.Fields, fromCodexField(f))
		}
		out = append(out, doc)
	}
	return out, nil
}

func fromCodexField(f codex.Field) Field {
	out := Field{Tag: f.Tag}
	if reason, lossy := bibframe.LossyTag(f.Tag); lossy {
		out.Lossy = reason
	}
	if f.IsControl() {
		out.Value = f.Value
		return out
	}
	i1, i2 := f.Indicators()
	out.Ind1, out.Ind2 = string(i1), string(i2)
	for _, sf := range f.Subfields {
		out.Subfields = append(out.Subfields, Subfield{Code: string(sf.Code), Value: sf.Value})
	}
	return out
}

func toCodexField(f Field) (codex.Field, error) {
	if len(f.Tag) != 3 {
		return codex.Field{}, fmt.Errorf("%w: bad tag %q", ErrValidation, f.Tag)
	}
	if f.Tag < "010" {
		return codex.NewControlField(f.Tag, f.Value), nil
	}
	ind := func(s string) byte {
		if s == "" {
			return ' '
		}
		return s[0]
	}
	out := codex.Field{Tag: f.Tag, Ind1: ind(f.Ind1), Ind2: ind(f.Ind2)}
	for _, sf := range f.Subfields {
		if sf.Code == "" {
			return codex.Field{}, fmt.Errorf("%w: %s has a subfield without a code", ErrValidation, f.Tag)
		}
		out.Subfields = append(out.Subfields, codex.NewSubfield(sf.Code[0], sf.Value))
	}
	if len(out.Subfields) == 0 {
		return codex.Field{}, fmt.Errorf("%w: %s has no subfields", ErrValidation, f.Tag)
	}
	return out, nil
}

// Save writes an edited record back onto the grain as an editorial diff and
// returns the updated grain (the input bytes unchanged when nothing
// changed). index addresses the record within View's order.
func Save(grain []byte, index int, edited RecordDoc) ([]byte, error) {
	docs, err := View(grain)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(docs) {
		return nil, fmt.Errorf("%w: no record %d", ErrValidation, index)
	}
	orig := docs[index]
	node := orig.Node
	workID, instanceID, err := recordIDs(grain, node)
	if err != nil {
		return nil, err
	}

	// Lossy tags live in the sidecar, everything else in the graph.
	origRec, origVerbatim, err := splitFields(orig)
	if err != nil {
		return nil, err
	}
	editedRec, editedVerbatim, err := splitFields(edited)
	if err != nil {
		return nil, err
	}

	patch := bibframe.Patch{}
	if err := diffGraphs(&patch, grain, origRec, editedRec, workID, instanceID); err != nil {
		return nil, err
	}
	if err := diffVerbatim(&patch, grain, node, origVerbatim, editedVerbatim); err != nil {
		return nil, err
	}
	if len(patch.Add) == 0 && len(patch.Remove) == 0 {
		return grain, nil // untouched: byte-identical no-op
	}
	return bibframe.ApplyEditorialPatch(grain, patch)
}

// splitFields builds the codex record for the graph diff (leader + non-lossy
// fields) and the sorted verbatim serializations of the lossy ones.
func splitFields(doc RecordDoc) (*codex.Record, []string, error) {
	rec := codex.NewRecord()
	if doc.Leader != "" {
		rec.SetLeader(codex.Leader(doc.Leader))
	}
	var verbatim []string
	for _, f := range doc.Fields {
		cf, err := toCodexField(f)
		if err != nil {
			return nil, nil, err
		}
		if _, lossy := bibframe.LossyTag(f.Tag); lossy {
			verbatim = append(verbatim, bibframe.EncodeVerbatimField(cf))
			continue
		}
		rec.AddField(cf)
	}
	sort.Strings(verbatim)
	return rec, verbatim, nil
}

// recordIDs finds the Work id owning node (and the instance id when node is
// an Instance) so the re-encoded graphs share the grain's IRIs.
func recordIDs(grain []byte, node string) (workID, instanceID string, err error) {
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return "", "", err
	}
	const bfNS = "http://id.loc.gov/ontologies/bibframe/"
	if strings.HasSuffix(node, "Work") {
		return trim(node, "Work"), "", nil
	}
	instanceID = trim(node, "Instance")
	for _, q := range ds.Quads {
		if q.P.Value == bfNS+"hasInstance" && q.O.Value == node {
			return trim(q.S.Value, "Work"), instanceID, nil
		}
		if q.P.Value == bfNS+"instanceOf" && q.S.Value == node {
			return trim(q.O.Value, "Work"), instanceID, nil
		}
	}
	return "", "", fmt.Errorf("marcview: no Work owns %s", node)
}

func trim(iri, suffix string) string {
	return strings.TrimSuffix(strings.TrimPrefix(iri, "#"), suffix)
}

// diffGraphs re-crosswalks both records onto the grain's node IRIs, groups
// triples by their IRI-rooted (subject, predicate) attachment, and turns
// each changed group into the override write shape: the lcat:overrides
// claim, retraction of the group's prior editorial statements (including
// stale skolem subtrees from earlier MARC saves), and the edited values
// re-asserted editorially (blank structures skolemized by content, so
// identical content keeps identical names across saves).
func diffGraphs(patch *bibframe.Patch, grain []byte, orig, edited *codex.Record, workID, instanceID string) error {
	build := func(rec *codex.Record) *rdf.Graph {
		bib := codexbf.FromRecord(rec)
		wi := codexbf.WorkInstances{Work: bib.Work}
		bases := []string{}
		if instanceID != "" {
			wi.Instances = []codexbf.Instance{bib.Instance}
			bases = []string{instanceID}
		}
		return wi.Graph(workID, bases)
	}
	origG, editedG := build(orig), build(edited)
	origGroups, editedGroups := groupTriples(origG), groupTriples(editedG)

	changed := map[groupKey]bool{}
	for key, vals := range editedGroups {
		if !sameMultiset(origGroups[key], vals) {
			changed[key] = true
		}
	}
	for key := range origGroups {
		if _, ok := editedGroups[key]; !ok {
			changed[key] = true
		}
	}
	if len(changed) == 0 {
		return nil
	}

	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return err
	}
	editorial := bibframe.EditorialGraph()
	keys := make([]groupKey, 0, len(changed))
	for key := range changed {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].subject != keys[j].subject {
			return keys[i].subject < keys[j].subject
		}
		return keys[i].pred < keys[j].pred
	})
	for _, key := range keys {
		// Claim the property: feed values shadow away, editorial owns it.
		claim := bibframe.OverridePatch(key.subject, key.pred)
		patch.Add = append(patch.Add, claim.Add...)
		// Retract the group's prior editorial statements + stale skolems.
		prefix := skolemPrefix(key)
		for _, q := range ds.Quads {
			if q.G != editorial {
				continue
			}
			direct := q.S.Value == key.subject && q.P.Value == key.pred && q.P.Value != bibframe.PredOverrides
			stale := strings.HasPrefix(q.S.Value, prefix) || (q.O.IsIRI() && strings.HasPrefix(q.O.Value, prefix))
			if direct || stale {
				patch.Remove = append(patch.Remove, q)
			}
		}
		// Re-assert the edited values, skolemizing blank structures.
		for _, tr := range editedG.Triples {
			if tr.S.Value != key.subject || tr.P.Value != key.pred || !tr.S.IsIRI() {
				continue
			}
			if tr.O.IsBlank() {
				skolem := skolemFor(key, editedG, tr.O)
				patch.Add = append(patch.Add, rdf.Quad{S: tr.S, P: tr.P, O: rdf.NewIRI(skolem)})
				appendSubtree(patch, key, editedG, tr.O, skolem, 0)
				continue
			}
			patch.Add = append(patch.Add, rdf.Quad{S: tr.S, P: tr.P, O: tr.O})
		}
	}
	return nil
}

// groupKey is one IRI-rooted attachment edge: subject IRI + predicate.
type groupKey struct{ subject, pred string }

// groupTriples collects the multiset of values per IRI-subject group; blank
// objects are keyed by their subtree's canonical content.
func groupTriples(g *rdf.Graph) map[groupKey][]string {
	out := map[groupKey][]string{}
	for _, tr := range g.Triples {
		if !tr.S.IsIRI() {
			continue
		}
		key := groupKey{tr.S.Value, tr.P.Value}
		out[key] = append(out[key], valueKey(g, tr.O, 0))
	}
	return out
}

// valueKey canonically names one object: literals and IRIs by their term,
// blanks by their subtree content (sorted, recursive, depth-bounded).
func valueKey(g *rdf.Graph, o rdf.Term, depth int) string {
	if !o.IsBlank() {
		return fmt.Sprintf("%d:%s@%s^%s", kindOf(o), o.Value, o.Lang, o.Datatype)
	}
	if depth > 8 {
		return "deep"
	}
	var lines []string
	for _, tr := range g.Triples {
		if tr.S == o {
			lines = append(lines, tr.P.Value+"\x00"+valueKey(g, tr.O, depth+1))
		}
	}
	sort.Strings(lines)
	return "~" + strings.Join(lines, "\x01")
}

func kindOf(o rdf.Term) int {
	if o.IsIRI() {
		return 1
	}
	return 2
}

func sameMultiset(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, v := range a {
		counts[v]++
	}
	for _, v := range b {
		counts[v]--
		if counts[v] < 0 {
			return false
		}
	}
	return true
}

// skolemPrefix names the editorial IRI namespace one group's structures live
// under, so a later save can retract exactly its own stale subtrees.
func skolemPrefix(key groupKey) string {
	return key.subject + "-marc-" + localName(key.pred) + "-"
}

// skolemFor mints the deterministic IRI for one blank structure: the group's
// namespace + the structure's content hash, so identical content names
// identically across saves.
func skolemFor(key groupKey, g *rdf.Graph, o rdf.Term) string {
	sum := sha256.Sum256([]byte(valueKey(g, o, 0)))
	return skolemPrefix(key) + hex.EncodeToString(sum[:6])
}

// appendSubtree copies a blank structure into the patch under its skolem
// IRI, recursively skolemizing nested blanks.
func appendSubtree(patch *bibframe.Patch, key groupKey, g *rdf.Graph, node rdf.Term, skolem string, depth int) {
	if depth > 8 {
		return
	}
	subject := rdf.NewIRI(skolem)
	for _, tr := range g.Triples {
		if tr.S != node {
			continue
		}
		if tr.O.IsBlank() {
			nested := skolem + "-" + shortHash(valueKey(g, tr.O, 0))
			patch.Add = append(patch.Add, rdf.Quad{S: subject, P: tr.P, O: rdf.NewIRI(nested)})
			appendSubtree(patch, key, g, tr.O, nested, depth+1)
			continue
		}
		patch.Add = append(patch.Add, rdf.Quad{S: subject, P: tr.P, O: tr.O})
	}
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}

func localName(pred string) string {
	if i := strings.LastIndexAny(pred, "/#"); i >= 0 {
		return pred[i+1:]
	}
	return pred
}

// diffVerbatim replaces the record's lossy-tag sidecar when the edited set
// differs from what the view showed: the claim shadows the feed copies and
// the edited set lands editorially.
func diffVerbatim(patch *bibframe.Patch, grain []byte, node string, orig, edited []string) error {
	if sameMultiset(orig, edited) {
		return nil
	}
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return err
	}
	claim := bibframe.OverridePatch(node, bibframe.PredMARCVerbatim)
	patch.Add = append(patch.Add, claim.Add...)
	editorial := bibframe.EditorialGraph()
	for _, q := range ds.Quads {
		if q.G == editorial && q.S.Value == node && q.P.Value == bibframe.PredMARCVerbatim {
			patch.Remove = append(patch.Remove, q)
		}
	}
	subject := rdf.NewIRI(node)
	for _, f := range edited {
		patch.Add = append(patch.Add, rdf.Quad{
			S: subject, P: rdf.NewIRI(bibframe.PredMARCVerbatim), O: rdf.NewLiteral(f, "", ""),
		})
	}
	return nil
}
