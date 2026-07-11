package bibframe

import (
	"fmt"
	"sort"
	"strings"

	codex "github.com/freeeve/libcodex"
	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/rdf"
)

// PredMARCVerbatim carries a crosswalk-lossy MARC field verbatim on its
// Instance node: on MARC ingest the known-loss tags (fidelity.go)
// are serialized field-exact into the feed graph instead of silently
// dropping; the MARC view shows them, edits to them land editorially under
// the same predicate (with the override marker shadowing the feed
// copies), and MARC export re-attaches them so the original forms round-trip.
const PredMARCVerbatim = LcatNS + "marcVerbatim"

// Verbatim field serialization: MARC's own delimiters, so it is exact and
// trivially reversible. A data field is tag + ind1 + ind2 + one "\x1f"-led
// (code, value) run per subfield; a control field (tag < "010", no
// indicators or subfields) is tag + raw value.
const subfieldDelim = "\x1f"

// EncodeVerbatimField serializes one MARC field to the sidecar's stable
// literal form.
func EncodeVerbatimField(f codex.Field) string {
	if f.IsControl() {
		return f.Tag + f.Value
	}
	var b strings.Builder
	b.WriteString(f.Tag)
	ind1, ind2 := f.Indicators()
	b.WriteByte(ind1)
	b.WriteByte(ind2)
	for _, sf := range f.Subfields {
		b.WriteString(subfieldDelim)
		b.WriteByte(sf.Code)
		b.WriteString(sf.Value)
	}
	return b.String()
}

// DecodeVerbatimField parses the sidecar literal form back to a field.
func DecodeVerbatimField(s string) (codex.Field, error) {
	if len(s) < 3 {
		return codex.Field{}, fmt.Errorf("bibframe: verbatim field too short: %q", s)
	}
	tag := s[:3]
	if tag < "010" {
		return codex.NewControlField(tag, s[3:]), nil
	}
	if len(s) < 5 {
		return codex.Field{}, fmt.Errorf("bibframe: verbatim data field %q missing indicators", s)
	}
	f := codex.Field{Tag: tag, Ind1: s[3], Ind2: s[4]}
	for run := range strings.SplitSeq(s[5:], subfieldDelim) {
		if run == "" {
			continue
		}
		f.Subfields = append(f.Subfields, codex.NewSubfield(run[0], run[1:]))
	}
	return f, nil
}

// VerbatimFields serializes a record's known-loss fields (fidelity.go) --
// what a MARC ingest provider hands the grain writer for the sidecar.
func VerbatimFields(rec *codex.Record) []string {
	var out []string
	for _, f := range rec.Fields() {
		if _, lossy := LossyTag(f.Tag); lossy {
			out = append(out, EncodeVerbatimField(f))
		}
	}
	return out
}

// addInstanceVerbatim attaches an Instance's verbatim MARC fields to its
// graph as PredMARCVerbatim literals, in deterministic order.
func addInstanceVerbatim(g *rdf.Graph, instanceID string, fields []string) {
	if len(fields) == 0 {
		return
	}
	node := rdf.NewIRI(InstanceIRI(instanceID))
	ordered := append([]string(nil), fields...)
	sort.Strings(ordered)
	for _, f := range ordered {
		if f != "" {
			g.Add(node, rdf.NewIRI(PredMARCVerbatim), rdf.NewLiteral(f, "", ""))
		}
	}
}

// DecodeGrainMARC materializes a grain's MARC records the framework-aware
// way: editorial lcat:overrides shadow the feed statements they
// claim (so an edited field decodes to its editorial value, not both), and
// each record's verbatim sidecar fields are re-attached in tag order. The
// mapping of records to instance nodes mirrors libcodex Decode's one-record-
// per-Work+Instance enumeration; if the counts ever disagree, verbatim
// attachment is skipped rather than misattached.
func DecodeGrainMARC(grain []byte) ([]*codex.Record, error) {
	recs, _, err := decodeGrainMARC(grain)
	return recs, err
}

// decodeGrainMARC is DecodeGrainMARC plus whether the grain carries any
// editorial-graph statement -- the fact the cataloging-source
// derivation reads.
func decodeGrainMARC(grain []byte) ([]*codex.Record, bool, error) {
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return nil, false, err
	}
	edited := false
	editorial := EditorialGraph()
	for _, q := range ds.Quads {
		if q.G == editorial {
			edited = true
			break
		}
	}
	overrides := ScanOverrides(ds)
	verbatim := map[string][]string{}
	keep := ds.Quads[:0]
	for _, q := range ds.Quads {
		shadowed := isFeedGraph(q.G) && q.S.IsIRI() && overrides.Shadows(q.S.Value, q.P.Value)
		if q.P.Value == PredMARCVerbatim {
			if q.O.IsLiteral() && q.S.IsIRI() && !shadowed {
				verbatim[q.S.Value] = append(verbatim[q.S.Value], q.O.Value)
			}
			continue // never feed lcat sidecar statements to the decoder
		}
		if shadowed {
			continue
		}
		keep = append(keep, q)
	}
	ds.Quads = keep
	// SKOS-shaped controlled subjects and their $0 authority links are read
	// natively by the crosswalk since libcodex v0.15.0 ( filed it;
	// retired the decode-local shim that used to live here). The
	// crosswalk still mints one heading PER prefLabel with no language
	// preference (libcodex), so multilingual terms are filtered to
	// their preferred label for this decode only.
	filterPreferredSubjectLabels(ds)
	var enc rdf.Encoder
	var doc []byte
	for _, gt := range ds.Graphs() {
		doc = enc.AppendNQuads(doc, ds.Graph(gt), gt)
	}
	recs, err := codexbf.Decode(doc)
	if err != nil {
		return nil, false, err
	}
	nodes := marcRecordNodes(ds)
	if len(nodes) == len(recs) {
		for i, rec := range recs {
			fields := append([]string(nil), verbatim[nodes[i]]...)
			sort.Strings(fields)
			for _, raw := range fields {
				f, err := DecodeVerbatimField(raw)
				if err != nil {
					return nil, false, fmt.Errorf("bibframe: bad verbatim sidecar on %s: %w", nodes[i], err)
				}
				rec.AddField(f)
			}
		}
	}
	return recs, edited, nil
}

// filterPreferredSubjectLabels keeps one prefLabel language per bf:subject
// IRI node for this decode: libcodex v0.15.0 reads SKOS subjects natively
// but mints one heading PER prefLabel with no language preference (their
// tracks the fix -- delete this filter when it lands). The pick
// is English, then untagged, then the first tag sorted (the label-pick
// order every label-bearing shape shares). Distinct labels in
// the chosen language are left alone.
func filterPreferredSubjectLabels(ds *rdf.Dataset) {
	subjects := map[string]bool{}
	for _, q := range ds.Quads {
		if q.P.Value == bfSubjectIRI && q.O.IsIRI() {
			subjects[q.O.Value] = true
		}
	}
	langs := map[string]map[string]bool{}
	for _, q := range ds.Quads {
		if q.P.Value == skosPrefLabelIRI && q.S.IsIRI() && q.O.IsLiteral() && subjects[q.S.Value] {
			set := langs[q.S.Value]
			if set == nil {
				set = map[string]bool{}
				langs[q.S.Value] = set
			}
			set[q.O.Lang] = true
		}
	}
	pick := map[string]string{}
	for uri, set := range langs {
		if len(set) < 2 {
			continue
		}
		switch {
		case set["en"]:
			pick[uri] = "en"
		case set[""]:
			pick[uri] = ""
		default:
			tags := make([]string, 0, len(set))
			for l := range set {
				tags = append(tags, l)
			}
			sort.Strings(tags)
			pick[uri] = tags[0]
		}
	}
	if len(pick) == 0 {
		return
	}
	keep := ds.Quads[:0]
	for _, q := range ds.Quads {
		if lang, ok := pick[q.S.Value]; ok && q.P.Value == skosPrefLabelIRI && q.O.IsLiteral() && q.O.Lang != lang {
			continue
		}
		keep = append(keep, q)
	}
	ds.Quads = keep
}

// MARCRecordNodes exposes the record -> instance-node mapping DecodeGrainMARC
// uses, so callers (the MARC view) can address a specific record's grain
// nodes. Node i corresponds to record i of DecodeGrainMARC; a Work-only
// record maps to its Work node.
func MARCRecordNodes(grain []byte) ([]string, error) {
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return nil, err
	}
	return marcRecordNodes(ds), nil
}

// marcRecordNodes mirrors libcodex Decode's record enumeration over the
// dataset: one record per Work+Instance pair in document order (a Work
// without Instances yields the Work node itself); Works that are the target
// of bf:relatedTo/bf:relation links are nested fields, not records.
func marcRecordNodes(ds *rdf.Dataset) []string {
	const (
		bfNS                = "http://id.loc.gov/ontologies/bibframe/"
		rdfType             = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
		pRelatedTo          = bfNS + "relatedTo"
		pAssociatedResource = bfNS + "associatedResource"
	)
	// Iterate in the exact order DecodeGrainMARC serializes the document
	// (per graph, then that graph's triples), so this enumeration and the
	// decoder's see the same document order.
	var ordered []rdf.Triple
	for _, gt := range ds.Graphs() {
		ordered = append(ordered, ds.Graph(gt).Triples...)
	}
	related := map[rdf.Term]bool{}
	for _, t := range ordered {
		if t.P.Value == pRelatedTo || t.P.Value == pAssociatedResource {
			related[t.O] = true
		}
	}
	instances := map[rdf.Term][]rdf.Term{}
	seen := map[[2]rdf.Term]bool{}
	link := func(work, inst rdf.Term) {
		key := [2]rdf.Term{work, inst}
		if !seen[key] {
			seen[key] = true
			instances[work] = append(instances[work], inst)
		}
	}
	var works []rdf.Term
	for _, t := range ordered {
		switch t.P.Value {
		case rdfType:
			if t.O.Value == bfNS+"Work" && t.O.IsIRI() {
				works = append(works, t.S)
			}
		case bfNS + "hasInstance":
			link(t.S, t.O)
		case bfNS + "instanceOf":
			link(t.O, t.S)
		}
	}
	var out []string
	emitted := map[rdf.Term]bool{}
	for _, work := range works {
		if related[work] || emitted[work] {
			continue
		}
		emitted[work] = true
		insts := instances[work]
		if len(insts) == 0 {
			out = append(out, work.Value)
			continue
		}
		for _, inst := range insts {
			out = append(out, inst.Value)
		}
	}
	return out
}

// isFeedGraph reports whether a graph term is a feed:<provider> named graph.
func isFeedGraph(g rdf.Term) bool {
	return g.IsIRI() && strings.HasPrefix(g.Value, "feed:")
}
