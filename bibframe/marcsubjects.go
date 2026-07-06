package bibframe

import (
	"sort"
	"strings"

	codex "github.com/freeeve/libcodex"
	"github.com/freeeve/libcodex/rdf"
)

// Crosswalk-facing subject vocabulary (tasks/136). The graph emission writes a
// controlled subject as <work> bf:subject <authority-iri> plus the IRI's
// skos:prefLabel; libcodex's MARC crosswalk reads rdf:type Topic + rdfs:label
// + bf:source instead, so without the shim below every controlled subject
// silently vanishes from MARC output.
const (
	predSubjRDFType = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
	predRDFSLabel   = "http://www.w3.org/2000/01/rdf-schema#label"
	predBFSource    = "http://id.loc.gov/ontologies/bibframe/source"
	typeBFTopic     = "http://id.loc.gov/ontologies/bibframe/Topic"
)

// subjectScheme maps an authority-IRI prefix to the MARC thesaurus code the
// crosswalk carries into the 6xx second indicator / $2 (via a bf:source node).
// An IRI outside every known prefix gets no source: ind2 stays blank, $2 absent.
var subjectSchemes = []struct{ prefix, code, source string }{
	{"https://homosaurus.org/", "homosaurus", "https://homosaurus.org/"},
	{"http://id.worldcat.org/fast/", "fast", "http://id.worldcat.org/fast"},
	{"https://id.worldcat.org/fast/", "fast", "http://id.worldcat.org/fast"},
	{"http://id.loc.gov/authorities/subjects/", "lcsh", "http://id.loc.gov/authorities/subjects"},
	{"https://id.loc.gov/authorities/subjects/", "lcsh", "http://id.loc.gov/authorities/subjects"},
}

// shimControlledSubjects makes SKOS-shaped controlled subjects readable by the
// libcodex MARC crosswalk (tasks/136): every bf:subject object that carries a
// skos:prefLabel but no rdfs:label gains, in its own graph, an rdf:type
// bf:Topic, an rdfs:label with its preferred label (English first), and --
// when the IRI belongs to a known authority -- a bf:source node labeled with
// the thesaurus code, yielding `650 _7 $a Label $2 code`. The dataset is
// mutated in place (the shim quads exist only for this decode, never in the
// stored grain). The returned map carries each unambiguous heading -> IRI for
// $0 injection; a heading shared by two IRIs is dropped from it.
func shimControlledSubjects(ds *rdf.Dataset) map[string]string {
	type subjectInfo struct {
		graph      rdf.Term
		hasLabel   bool
		hasType    bool
		prefByLang map[string]string
	}
	subjects := map[string]*subjectInfo{}
	var order []string
	for _, q := range ds.Quads {
		if q.P.Value == predSubject && q.O.IsIRI() {
			if _, seen := subjects[q.O.Value]; !seen {
				subjects[q.O.Value] = &subjectInfo{graph: q.G, prefByLang: map[string]string{}}
				order = append(order, q.O.Value)
			}
		}
	}
	if len(subjects) == 0 {
		return nil
	}
	for _, q := range ds.Quads {
		info := subjects[q.S.Value]
		if info == nil || !q.S.IsIRI() {
			continue
		}
		switch q.P.Value {
		case predRDFSLabel:
			info.hasLabel = true
		case predSubjRDFType:
			info.hasType = true
		case predPrefLabel:
			if q.O.IsLiteral() && q.O.Value != "" {
				if _, dup := info.prefByLang[q.O.Lang]; !dup {
					info.prefByLang[q.O.Lang] = q.O.Value
				}
			}
		}
	}
	sort.Strings(order)
	byLabel := map[string]string{}
	ambiguous := map[string]bool{}
	for _, iri := range order {
		info := subjects[iri]
		label := preferredLabel(info.prefByLang)
		if info.hasLabel || label == "" {
			continue
		}
		node := rdf.NewIRI(iri)
		add := func(p string, o rdf.Term) {
			ds.Quads = append(ds.Quads, rdf.Quad{S: node, P: rdf.NewIRI(p), O: o, G: info.graph})
		}
		if !info.hasType {
			add(predSubjRDFType, rdf.NewIRI(typeBFTopic))
		}
		add(predRDFSLabel, rdf.NewLiteral(label, "", ""))
		for _, s := range subjectSchemes {
			if strings.HasPrefix(iri, s.prefix) {
				src := rdf.NewIRI(s.source)
				add(predBFSource, src)
				ds.Quads = append(ds.Quads, rdf.Quad{
					S: src, P: rdf.NewIRI(predRDFSLabel), O: rdf.NewLiteral(s.code, "", ""), G: info.graph,
				})
				break
			}
		}
		if prev, taken := byLabel[label]; taken && prev != iri {
			ambiguous[label] = true
		}
		byLabel[label] = iri
	}
	for label := range ambiguous {
		delete(byLabel, label)
	}
	return byLabel
}

// preferredLabel picks the single label MARC gets: English when present,
// otherwise the lexicographically first language tag (untagged sorts first).
func preferredLabel(byLang map[string]string) string {
	if l := byLang["en"]; l != "" {
		return l
	}
	langs := make([]string, 0, len(byLang))
	for lang := range byLang {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		if byLang[lang] != "" {
			return byLang[lang]
		}
	}
	return ""
}

// injectSubjectAuthorityIDs appends `$0 <authority-iri>` to each crosswalk-
// produced 6xx heading whose reconstructed label matches an unambiguous
// controlled subject (tasks/136) -- the linkage ILS consumers actually keep.
// Runs before verbatim sidecar fields re-attach, so byte-preserved originals
// are never rewritten. Fields already carrying a $0 are left alone.
func injectSubjectAuthorityIDs(recs []*codex.Record, byLabel map[string]string) {
	if len(byLabel) == 0 {
		return
	}
	for _, rec := range recs {
		fields := rec.Fields()
		for i := range fields {
			switch fields[i].Tag {
			case "600", "610", "611", "650", "651":
			default:
				continue
			}
			if _, has := fields[i].Subfield('0'); has {
				continue
			}
			var parts []string
			for _, sf := range fields[i].Subfields {
				if sf.Code == 'a' || sf.Code == 'x' {
					parts = append(parts, sf.Value)
				}
			}
			if iri := byLabel[strings.Join(parts, "--")]; iri != "" {
				fields[i].Subfields = append(fields[i].Subfields, codex.NewSubfield('0', iri))
			}
		}
	}
}
