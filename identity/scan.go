package identity

import (
	"strings"

	"github.com/freeeve/libcodex/rdf"
)

// Provider-key schemes namespace an identifier value so keys from different
// schemes never collide, and so a key recovered from a grain matches the key
// ingest builds for the same identifier.
const (
	SchemeISBN = "isbn"
	SchemeISSN = "issn"
	SchemeID   = "id" // provider-local identifier (e.g. an OverDrive title/reserve id)
)

// ProviderKey namespaces an identifier value by scheme, e.g.
// ProviderKey(SchemeISBN, "978...") -> "isbn:978...".
func ProviderKey(scheme, value string) string { return scheme + ":" + value }

// BIBFRAME / RDF IRIs the scanner reads. Kept local so identity depends only on
// the rdf toolkit, not on the bibframe crosswalk.
const (
	bfNS               = "http://id.loc.gov/ontologies/bibframe/"
	bfWork             = bfNS + "Work"
	bfInstance         = bfNS + "Instance"
	bfInstanceOf       = bfNS + "instanceOf"
	bfIdentifiedBy     = bfNS + "identifiedBy"
	bfIsbn             = bfNS + "Isbn"
	bfIssn             = bfNS + "Issn"
	bfTitle            = bfNS + "title"
	bfMainTitle        = bfNS + "mainTitle"
	bfContribution     = bfNS + "contribution"
	bfAgent            = bfNS + "agent"
	bfLanguage         = bfNS + "language"
	bflcPrimaryContrib = "http://id.loc.gov/ontologies/bflc/PrimaryContribution"
	rdfValue           = "http://www.w3.org/1999/02/22-rdf-syntax-ns#value"
	rdfsLabel          = "http://www.w3.org/2000/01/rdf-schema#label"
)

// InstanceIdentity is the committed identity of one Instance recovered from a
// grain: its minted id, the Work it belongs to, and the provider keys it answers
// to.
type InstanceIdentity struct {
	InstanceID   string
	WorkID       string
	ProviderKeys []string
}

// WorkIdentity is the committed identity of one Work recovered from a grain: its
// minted id and its recomputed clustering key, so a new record with the same key
// clusters onto it rather than minting a fresh Work.
type WorkIdentity struct {
	WorkID     string
	ClusterKey string
}

// GrainIdentity is the identity recovered from one grain -- the Work(s) it
// carries and their Instances. The derive-from-grains model (Decision A,
// ): the committed grains are themselves the identity map.
type GrainIdentity struct {
	Works     []WorkIdentity
	Instances []InstanceIdentity
}

// ScanGrain recovers the identities committed in one grain's N-Quads. Node ids
// come from the #<id>Work / #<id>Instance IRIs; a Work's clustering key is
// recomputed from its primary author, main title, and language; provider keys
// come from each bf:identifiedBy value, namespaced by its identifier type. It
// reads every named graph, since a grain's feed and editorial lines share the
// file. Only minted fragment IRIs count: the crosswalk types 76X-78X
// related-resource stubs as bf:Work/bf:Instance too (blank or external
// nodes), and seeding their keys or identifiers would let an unrelated
// incoming record resolve onto a stub label instead of minting.
func ScanGrain(nq []byte) (GrainIdentity, error) {
	ds, err := rdf.ParseNQuads(nq)
	if err != nil {
		return GrainIdentity{}, err
	}
	return ScanDataset(ds), nil
}

// ScanDataset is ScanGrain for callers that already hold the parsed dataset
// (the work index scans identity, summaries, and barcodes off one parse).
func ScanDataset(ds *rdf.Dataset) GrainIdentity {
	var gi GrainIdentity
	for _, gt := range ds.Graphs() {
		// Zero-copy per-graph view (libcodex v0.19.0): the
		// per-graph query semantics -- feed vs editorial separation -- are
		// unchanged; only the materialized []Triple copy per graph is gone.
		g := ds.GraphView(gt)
		for _, work := range g.SubjectsOfType(bfWork) {
			if !minted(work, "Work") {
				continue
			}
			gi.Works = append(gi.Works, WorkIdentity{
				WorkID:     fragID(work.Value, "Work"),
				ClusterKey: WorkKey(workAuthor(g, work), workTitle(g, work), workLang(g, work)),
			})
		}
		for _, inst := range g.SubjectsOfType(bfInstance) {
			if !minted(inst, "Instance") {
				continue
			}
			id := InstanceIdentity{InstanceID: fragID(inst.Value, "Instance")}
			if w, ok := g.Object(inst, bfInstanceOf); ok {
				id.WorkID = fragID(w.Value, "Work")
			}
			for _, node := range g.Objects(inst, bfIdentifiedBy) {
				if val, ok := g.Literal(node, rdfValue); ok && val != "" {
					id.ProviderKeys = append(id.ProviderKeys, ProviderKey(identifierScheme(g, node), val))
				}
			}
			gi.Instances = append(gi.Instances, id)
		}
	}
	return gi
}

// minted reports whether a node is a catalog-minted entity: the "#<id>Work" /
// "#<id>Instance" fragment-IRI convention, id non-empty.
func minted(t rdf.Term, suffix string) bool {
	return t.IsIRI() && strings.HasPrefix(t.Value, "#") && strings.HasSuffix(t.Value, suffix) &&
		len(t.Value) > len(suffix)+1
}

// SeedResolver seeds r with the committed identity from scanned grains, so a
// subsequent Resolve reuses existing ids instead of re-minting.
func SeedResolver(r *Resolver, grains []GrainIdentity) {
	for _, gi := range grains {
		for _, w := range gi.Works {
			r.SeedWorkKey(w.ClusterKey, w.WorkID)
		}
		for _, inst := range gi.Instances {
			r.SeedInstance(inst.InstanceID, inst.WorkID, inst.ProviderKeys)
		}
	}
}

// fragID extracts a minted id from a node IRI of the form "#<id><suffix>".
func fragID(iri, suffix string) string {
	return strings.TrimSuffix(strings.TrimPrefix(iri, "#"), suffix)
}

// identifierScheme namespaces an identifier by its BIBFRAME type: bf:Isbn ->
// isbn, bf:Issn -> issn, anything else -> id.
func identifierScheme(g rdf.GraphQuery, node rdf.Term) string {
	switch {
	case g.HasType(node, bfIsbn):
		return SchemeISBN
	case g.HasType(node, bfIssn):
		return SchemeISSN
	default:
		return SchemeID
	}
}

// workAuthor returns the label of a Work's primary contribution agent, or "".
func workAuthor(g rdf.GraphQuery, work rdf.Term) string {
	for _, c := range g.Objects(work, bfContribution) {
		if !g.HasType(c, bflcPrimaryContrib) {
			continue
		}
		if agent, ok := g.Object(c, bfAgent); ok {
			if label, ok := g.Literal(agent, rdfsLabel); ok {
				return label
			}
		}
	}
	return ""
}

// workTitle returns a Work's main title, or "".
func workTitle(g rdf.GraphQuery, work rdf.Term) string {
	for _, t := range g.Objects(work, bfTitle) {
		if mt, ok := g.Literal(t, bfMainTitle); ok {
			return mt
		}
	}
	return ""
}

// workLang returns a Work's language code (the local name of its language URI),
// or "".
func workLang(g rdf.GraphQuery, work rdf.Term) string {
	if l, ok := g.Object(work, bfLanguage); ok {
		return rdf.LocalName(l.Value)
	}
	return ""
}
