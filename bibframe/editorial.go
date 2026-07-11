package bibframe

import (
	"errors"
	"fmt"
	"sort"

	"github.com/freeeve/libcodex/rdf"
)

// Vocabulary IRIs for the editorial write path.
const (
	bfSubjectIRI     = "http://id.loc.gov/ontologies/bibframe/subject"
	skosPrefLabelIRI = "http://www.w3.org/2004/02/skos/core#prefLabel"
	skosBroaderIRI   = "http://www.w3.org/2004/02/skos/core#broader"
	owlSameAsIRI     = "http://www.w3.org/2002/07/owl#sameAs"
)

// SameAsQuad links a Work to an external hub resource that identifies the same
// work: <workURI> owl:sameAs <externalURI>. Minted `w…` ids stay the
// primary identity -- this is an attached outward link, written into an
// enrichment:<name> graph, never a re-derivation of the local id.
func SameAsQuad(workID, externalURI string) rdf.Quad {
	return rdf.Quad{
		S: rdf.NewIRI(WorkIRI(workID)),
		P: rdf.NewIRI(owlSameAsIRI),
		O: rdf.NewIRI(externalURI),
	}
}

// PredTag carries an uncontrolled folksonomy tag as a plain literal on a
// Work. Feed tags arrive as labeled blank bf:Topic nodes, but editorial-class
// statements must be blank-free, so approved community tags use this
// predicate instead; the projector folds both shapes into the same Tags
// dimension.
const PredTag = LcatNS + "tag"

// TagQuad builds the editorial statement for one approved folksonomy tag.
func TagQuad(workID, tag string) rdf.Quad {
	return rdf.Quad{
		S: rdf.NewIRI(WorkIRI(workID)),
		P: rdf.NewIRI(PredTag),
		O: rdf.NewLiteral(tag, "", ""),
	}
}

// SubjectQuad builds the editorial statement linking a Work to a controlled
// subject's authority URI.
func SubjectQuad(workID, termURI string) rdf.Quad {
	return rdf.Quad{
		S: rdf.NewIRI(WorkIRI(workID)),
		P: rdf.NewIRI(bfSubjectIRI),
		O: rdf.NewIRI(termURI),
	}
}

// PredTagAlias records that a controlled term subsumes an uncontrolled tag:
// <termURI> lcat:tagAlias "tag" in the alias graph. Written when a
// folksonomy/feed tag is promoted to a controlled term; the
// projector then suppresses the tag on Works that carry the aliasing subject
// (the tag "became" the subject), and pickers auto-suggest the controlled
// term when the tag is typed. The projector indexes the predicate across
// every graph, so the statement's home graph is bookkeeping only.
const PredTagAlias = LcatNS + "tagAlias"

// AliasGraph names the graph tag-alias bookkeeping lives in. Deliberately
// OUTSIDE the authority:<vocab> namespace: the vocab loader routes every
// authority:-prefixed graph to a scheme, and alias statements carry no
// labels, so an authority-class home minted a bogus labelless "aliases"
// vocabulary that shadowed the promoted terms.
func AliasGraph() rdf.Term { return rdf.NewIRI("lcat:aliases") }

// TagAliasQuad builds the alias statement.
func TagAliasQuad(termURI, tag string) rdf.Quad {
	return rdf.Quad{
		S: rdf.NewIRI(termURI),
		P: rdf.NewIRI(PredTagAlias),
		O: rdf.NewLiteral(tag, "", ""),
	}
}

// AuthorityGraph returns the named-graph term for a controlled vocabulary's
// statements: the authority:<vocab> provenance class from ARCHITECTURE §5
// (term labels, definitions, hierarchy). Like editorial:, authority graphs
// are preserved verbatim across feed re-ingest.
func AuthorityGraph(vocab string) rdf.Term {
	return rdf.NewIRI("authority:" + vocab)
}

// EnrichmentGraph returns the named-graph term for a machine-enrichment
// source's statements. Enrichment graphs are regenerable -- an
// enricher drop-and-replaces its own graph on each run -- but, not being
// feed:<provider>, they are preserved across feed re-ingest like editorial
// statements.
func EnrichmentGraph(name string) rdf.Term {
	return rdf.NewIRI("enrichment:" + name)
}

// Patch is a set of statement additions and removals applied to one
// provenance graph of a grain. Each quad's own G field is ignored: the
// target graph is chosen by the applying function. Subjects and objects must
// be IRIs or literals -- blank nodes are rejected because editorial-class
// statements must not introduce blank labels that could collide with the
// feed graph's during joint canonicalization (see grainWithEditorial).
type Patch struct {
	Add    []rdf.Quad
	Remove []rdf.Quad
}

// ErrBlankNode reports a patch quad using a blank node.
var ErrBlankNode = errors.New("bibframe: editorial patches must not use blank nodes")

// ApplyEditorialPatch applies p to the grain's editorial: graph and returns
// the re-canonicalized grain -- the general form of the merge/split marker
// writers, and the write path record editing and suggestion publishing share.
// Additions are idempotent (an already-present statement is not duplicated);
// removals match exact statements in the editorial graph only.
func ApplyEditorialPatch(grainNQ []byte, p Patch) ([]byte, error) {
	return ApplyPatch(grainNQ, EditorialGraph(), p)
}

// ApplyPatch applies p to one named graph of the grain and returns the
// re-canonicalized grain.
func ApplyPatch(grainNQ []byte, graph rdf.Term, p Patch) ([]byte, error) {
	if err := checkPatchTerms(p); err != nil {
		return nil, err
	}
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	if len(p.Remove) > 0 {
		keep := ds.Quads[:0]
		for _, q := range ds.Quads {
			if q.G == graph && quadMatches(p.Remove, q) {
				continue
			}
			keep = append(keep, q)
		}
		ds.Quads = keep
	}
	for _, q := range p.Add {
		addUnique(ds, q.S, q.P, q.O, graph)
	}
	return ds.Canonical()
}

// ReplaceGraph drops every statement in the given named graph and replaces
// them with quads (whose own G fields are ignored), returning the
// re-canonicalized grain. This is the idempotent re-enrichment primitive: an
// enricher owns its enrichment:<name> graph outright.
func ReplaceGraph(grainNQ []byte, graph rdf.Term, quads []rdf.Quad) ([]byte, error) {
	if err := checkPatchTerms(Patch{Add: quads}); err != nil {
		return nil, err
	}
	ds, err := rdf.ParseNQuads(grainNQ)
	if err != nil {
		return nil, err
	}
	keep := ds.Quads[:0]
	for _, q := range ds.Quads {
		if q.G != graph {
			keep = append(keep, q)
		}
	}
	ds.Quads = keep
	for _, q := range quads {
		addUnique(ds, q.S, q.P, q.O, graph)
	}
	return ds.Canonical()
}

// AppendAuthoritySubject records a controlled subject on a Work: the
// bf:subject link lands in the editorial: graph (a human/reviewed decision),
// while the term's own description (prefLabel per language, broader links)
// lands in the vocabulary's authority:<vocab> graph so the projector's label
// index can resolve it. Idempotent.
func AppendAuthoritySubject(grainNQ []byte, workID string, subj AuthoritySubject, vocab string) ([]byte, error) {
	if subj.URI == "" {
		return nil, fmt.Errorf("bibframe: authority subject needs a URI")
	}
	term := rdf.NewIRI(subj.URI)
	editorial := Patch{Add: []rdf.Quad{
		{S: rdf.NewIRI(WorkIRI(workID)), P: rdf.NewIRI(bfSubjectIRI), O: term},
	}}
	out, err := ApplyEditorialPatch(grainNQ, editorial)
	if err != nil {
		return nil, err
	}
	var authority Patch
	for lang, label := range subj.Labels {
		authority.Add = append(authority.Add, rdf.Quad{
			S: term, P: rdf.NewIRI(skosPrefLabelIRI), O: rdf.NewLiteral(label, lang, ""),
		})
	}
	for _, parent := range subj.Broader {
		authority.Add = append(authority.Add, rdf.Quad{
			S: term, P: rdf.NewIRI(skosBroaderIRI), O: rdf.NewIRI(parent),
		})
	}
	if len(authority.Add) == 0 {
		return out, nil
	}
	return ApplyPatch(out, AuthorityGraph(vocab), authority)
}

// AppendAuthorityTerms writes standalone term descriptions (prefLabel per
// language, broader links) into the authority:<vocab> graph without linking
// them to any Work -- skos:broader ancestor-chain metadata, so hierarchy
// nodes no Work carries directly still resolve labels in the projection
// . Terms with no metadata contribute nothing. Idempotent.
func AppendAuthorityTerms(grainNQ []byte, vocab string, terms []AuthoritySubject) ([]byte, error) {
	var authority Patch
	for _, subj := range terms {
		if subj.URI == "" {
			continue
		}
		term := rdf.NewIRI(subj.URI)
		langs := make([]string, 0, len(subj.Labels))
		for lang := range subj.Labels {
			langs = append(langs, lang)
		}
		sort.Strings(langs)
		for _, lang := range langs {
			authority.Add = append(authority.Add, rdf.Quad{
				S: term, P: rdf.NewIRI(skosPrefLabelIRI), O: rdf.NewLiteral(subj.Labels[lang], lang, ""),
			})
		}
		for _, parent := range subj.Broader {
			authority.Add = append(authority.Add, rdf.Quad{
				S: term, P: rdf.NewIRI(skosBroaderIRI), O: rdf.NewIRI(parent),
			})
		}
	}
	if len(authority.Add) == 0 {
		return grainNQ, nil
	}
	return ApplyPatch(grainNQ, AuthorityGraph(vocab), authority)
}

func checkPatchTerms(p Patch) error {
	for _, list := range [][]rdf.Quad{p.Add, p.Remove} {
		for _, q := range list {
			if q.S.IsBlank() || q.O.IsBlank() {
				return ErrBlankNode
			}
			if !q.S.IsIRI() || !q.P.IsIRI() {
				return fmt.Errorf("bibframe: patch subject and predicate must be IRIs")
			}
		}
	}
	return nil
}

func quadMatches(list []rdf.Quad, q rdf.Quad) bool {
	for _, r := range list {
		if r.S == q.S && r.P == q.P && r.O == q.O {
			return true
		}
	}
	return false
}
