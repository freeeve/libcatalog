package bibframe

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/freeeve/libcodex/rdf"
)

// LocalAuthorityNS is the IRI namespace for locally minted authority terms
// . Shared vocabularies (Homosaurus, LCSH) bring their own IRIs;
// a heading a deployment coins itself gets one under this namespace so
// bf:subject references stay absolute IRIs like any other controlled term.
const LocalAuthorityNS = "https://github.com/freeeve/libcat/authority/"

// SKOS predicate IRIs the authority grain writer emits (the read side lives
// in backend/vocab, which indexes the same statements).
const (
	skosAltLabelIRI   = "http://www.w3.org/2004/02/skos/core#altLabel"
	skosDefinitionIRI = "http://www.w3.org/2004/02/skos/core#definition"
	skosNarrowerIRI   = "http://www.w3.org/2004/02/skos/core#narrower"
	skosRelatedIRI    = "http://www.w3.org/2004/02/skos/core#related"
	skosExactMatchIRI = "http://www.w3.org/2004/02/skos/core#exactMatch"
)

// LocalAuthorityIRI returns the absolute IRI for a locally minted authority
// id (identity.Mint with the "a" prefix).
func LocalAuthorityIRI(id string) string { return LocalAuthorityNS + id }

// AuthorityGrainPath maps an authority id to its grain path in the repo
// layout: data/authorities/<xx>/<id>.nq, sharded like Work grains so no
// directory holds 100k+ entries (ARCHITECTURE §3).
func AuthorityGrainPath(id string) string {
	shard := hashID([]byte(id))[:2]
	return path.Join("data", "authorities", shard, id+".nq")
}

// AuthorityTerm is the editable description of one authority concept -- the
// SKOS fields the authority-topic profile exposes. Maps are
// keyed by language tag ("" = untagged).
type AuthorityTerm struct {
	URI        string              `json:"uri"`
	PrefLabel  map[string]string   `json:"prefLabel"`
	AltLabel   map[string][]string `json:"altLabel,omitempty"`
	Definition map[string]string   `json:"definition,omitempty"`
	Broader    []string            `json:"broader,omitempty"`
	Narrower   []string            `json:"narrower,omitempty"`
	Related    []string            `json:"related,omitempty"`
	ExactMatch []string            `json:"exactMatch,omitempty"`
	// MergedInto records the term's retirement: it was merged into the
	// referenced term (lcat:mergedInto). Preserved across description edits.
	MergedInto string `json:"mergedInto,omitempty"`
}

// Quads serializes the term description to its authority-graph statements.
// The graph field of the returned quads is unset; the caller chooses the
// authority:<vocab> graph.
func (t AuthorityTerm) Quads() ([]rdf.Quad, error) {
	if t.URI == "" {
		return nil, fmt.Errorf("bibframe: authority term needs a URI")
	}
	subj := rdf.NewIRI(t.URI)
	var out []rdf.Quad
	addLiterals := func(pred string, byLang map[string]string) {
		for _, lang := range sortedKeys(byLang) {
			out = append(out, rdf.Quad{S: subj, P: rdf.NewIRI(pred), O: rdf.NewLiteral(byLang[lang], lang, "")})
		}
	}
	addLiterals(skosPrefLabelIRI, t.PrefLabel)
	for _, lang := range sortedKeys(t.AltLabel) {
		for _, label := range t.AltLabel[lang] {
			out = append(out, rdf.Quad{S: subj, P: rdf.NewIRI(skosAltLabelIRI), O: rdf.NewLiteral(label, lang, "")})
		}
	}
	addLiterals(skosDefinitionIRI, t.Definition)
	addIRIs := func(pred string, uris []string) {
		for _, uri := range uris {
			out = append(out, rdf.Quad{S: subj, P: rdf.NewIRI(pred), O: rdf.NewIRI(uri)})
		}
	}
	addIRIs(skosBroaderIRI, t.Broader)
	addIRIs(skosNarrowerIRI, t.Narrower)
	addIRIs(skosRelatedIRI, t.Related)
	addIRIs(skosExactMatchIRI, t.ExactMatch)
	if t.MergedInto != "" {
		out = append(out, rdf.Quad{S: subj, P: rdf.NewIRI(PredMergedInto), O: rdf.NewIRI(t.MergedInto)})
	}
	return out, nil
}

// BuildAuthorityGrain (re)serializes a local authority term's grain: the
// term's statements replace the authority:<vocab> graph wholesale (the term
// owns its grain), preserving any statements other graphs might carry.
// oldGrain may be nil/empty for a fresh term.
func BuildAuthorityGrain(oldGrain []byte, t AuthorityTerm, vocab string) ([]byte, error) {
	quads, err := t.Quads()
	if err != nil {
		return nil, err
	}
	if oldGrain == nil {
		oldGrain = []byte{}
	}
	return ReplaceGraph(oldGrain, AuthorityGraph(vocab), quads)
}

// ParseAuthorityGrain recovers the term description for uri from an authority
// grain's authority:<vocab> graph -- the inverse of BuildAuthorityGrain.
func ParseAuthorityGrain(grainNQ []byte, uri, vocab string) (AuthorityTerm, error) {
	t := AuthorityTerm{URI: uri, PrefLabel: map[string]string{}}
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return t, err
	}
	graph := AuthorityGraph(vocab)
	for _, q := range ds.Quads {
		if q.G != graph || !q.S.IsIRI() || q.S.Value != uri {
			continue
		}
		switch q.P.Value {
		case skosPrefLabelIRI:
			if q.O.IsLiteral() {
				t.PrefLabel[q.O.Lang] = q.O.Value
			}
		case skosAltLabelIRI:
			if q.O.IsLiteral() {
				if t.AltLabel == nil {
					t.AltLabel = map[string][]string{}
				}
				t.AltLabel[q.O.Lang] = append(t.AltLabel[q.O.Lang], q.O.Value)
			}
		case skosDefinitionIRI:
			if q.O.IsLiteral() {
				if t.Definition == nil {
					t.Definition = map[string]string{}
				}
				t.Definition[q.O.Lang] = q.O.Value
			}
		case skosBroaderIRI:
			if q.O.IsIRI() {
				t.Broader = append(t.Broader, q.O.Value)
			}
		case skosNarrowerIRI:
			if q.O.IsIRI() {
				t.Narrower = append(t.Narrower, q.O.Value)
			}
		case skosRelatedIRI:
			if q.O.IsIRI() {
				t.Related = append(t.Related, q.O.Value)
			}
		case skosExactMatchIRI:
			if q.O.IsIRI() {
				t.ExactMatch = append(t.ExactMatch, q.O.Value)
			}
		case PredMergedInto:
			if q.O.IsIRI() {
				t.MergedInto = q.O.Value
			}
		}
	}
	sort.Strings(t.Broader)
	sort.Strings(t.Narrower)
	sort.Strings(t.Related)
	sort.Strings(t.ExactMatch)
	return t, nil
}

// AuthorityGrainDescribes reports whether the grain carries any statement
// about uri -- the pre-check a merge runs before asserting a
// marker, so a namespace-mismatched grain errors instead of gaining a
// phantom node.
func AuthorityGrainDescribes(grainNQ []byte, uri string) bool {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return false
	}
	subject := rdf.NewIRI(uri)
	for _, q := range ds.Quads {
		if q.S == subject {
			return true
		}
	}
	return false
}

// AddAuthorityMergeMarker retires an authority term: <loser> lcat:mergedInto
// <winner> lands in the loser grain's authority:<vocab> graph, so the vocab
// index sees the retirement on reload and the decision survives description
// edits. Idempotent. A loser the grain does not describe is refused: the
// marker would otherwise mint a phantom labelless node instead of retiring
// anything (reachable when a grain's subject IRI base differs
// from the id-derived one, e.g. pre-rename or imported namespaces).
func AddAuthorityMergeMarker(grainNQ []byte, loserURI, winnerURI, vocab string) ([]byte, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	loser := rdf.NewIRI(loserURI)
	described := false
	for _, q := range ds.Quads {
		if q.S == loser {
			described = true
			break
		}
	}
	if !described {
		return nil, fmt.Errorf("bibframe: authority grain does not describe %s", loserURI)
	}
	return ApplyPatch(grainNQ, AuthorityGraph(vocab), Patch{Add: []rdf.Quad{{
		S: loser,
		P: rdf.NewIRI(PredMergedInto),
		O: rdf.NewIRI(winnerURI),
	}}})
}

// ReplaceSubjectReference rewrites one Work's controlled-subject reference
// after an authority merge: the editorial bf:subject link to the
// loser is retracted along with every authority-graph statement about the
// loser the grain carries for the projector's label index, and the winner is
// appended through the same path suggestion publishing uses. Feed-graph
// statements are never touched (editorial rewrites do not reach into
// feed:<provider> graphs); a feed-asserted subject needs the
// override semantics instead.
func ReplaceSubjectReference(grainNQ []byte, workID, loserURI string, winner AuthoritySubject, winnerVocab string) ([]byte, error) {
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	loser := rdf.NewIRI(loserURI)
	editorial := EditorialGraph()
	keep := ds.Quads[:0]
	for _, q := range ds.Quads {
		if q.G == editorial && q.P.Value == bfSubjectIRI && q.O == loser {
			continue
		}
		if isAuthorityGraph(q.G) && (q.S == loser || q.O == loser) {
			continue
		}
		keep = append(keep, q)
	}
	ds.Quads = keep
	stripped, err := ds.Canonical()
	if err != nil {
		return nil, err
	}
	return AppendAuthoritySubject(stripped, workID, winner, winnerVocab)
}

// isAuthorityGraph reports whether a graph term is an authority:<vocab>
// named graph.
func isAuthorityGraph(g rdf.Term) bool {
	return g.IsIRI() && strings.HasPrefix(g.Value, "authority:")
}

// sortedKeys returns a map's keys in sorted order for deterministic quad
// emission.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
