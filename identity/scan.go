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
	bfNS           = "http://id.loc.gov/ontologies/bibframe/"
	bfInstance     = bfNS + "Instance"
	bfInstanceOf   = bfNS + "instanceOf"
	bfIdentifiedBy = bfNS + "identifiedBy"
	bfIsbn         = bfNS + "Isbn"
	bfIssn         = bfNS + "Issn"
	rdfValue       = "http://www.w3.org/1999/02/22-rdf-syntax-ns#value"
)

// InstanceIdentity is the committed identity of one Instance, recovered from a
// grain: its minted id, the Work it belongs to, and the provider keys it answers
// to. It seeds a Resolver so re-ingest resolves instead of re-minting -- the
// derive-from-grains model where the committed grains are themselves the
// identity map (ARCHITECTURE §4, tasks/002).
type InstanceIdentity struct {
	InstanceID   string
	WorkID       string
	ProviderKeys []string
}

// ScanGrain recovers the Instance identities committed in one grain's N-Quads.
// Node ids come from the #<id>Instance / #<id>Work IRIs; provider keys come from
// each bf:identifiedBy value, namespaced by its identifier type so they match the
// keys ingest builds. It reads every named graph, since a grain's feed and
// editorial lines share the file.
func ScanGrain(nq []byte) ([]InstanceIdentity, error) {
	ds, err := rdf.ParseNQuads(nq)
	if err != nil {
		return nil, err
	}
	var out []InstanceIdentity
	for _, gt := range ds.Graphs() {
		g := ds.Graph(gt)
		for _, inst := range g.SubjectsOfType(bfInstance) {
			id := InstanceIdentity{InstanceID: fragID(inst.Value, "Instance")}
			if w, ok := g.Object(inst, bfInstanceOf); ok {
				id.WorkID = fragID(w.Value, "Work")
			}
			for _, node := range g.Objects(inst, bfIdentifiedBy) {
				if val, ok := g.Literal(node, rdfValue); ok && val != "" {
					id.ProviderKeys = append(id.ProviderKeys, ProviderKey(identifierScheme(g, node), val))
				}
			}
			out = append(out, id)
		}
	}
	return out, nil
}

// fragID extracts a minted id from a node IRI of the form "#<id><suffix>".
func fragID(iri, suffix string) string {
	return strings.TrimSuffix(strings.TrimPrefix(iri, "#"), suffix)
}

// identifierScheme namespaces an identifier by its BIBFRAME type: bf:Isbn ->
// isbn, bf:Issn -> issn, anything else -> id.
func identifierScheme(g *rdf.Graph, node rdf.Term) string {
	switch {
	case g.HasType(node, bfIsbn):
		return SchemeISBN
	case g.HasType(node, bfIssn):
		return SchemeISSN
	default:
		return SchemeID
	}
}
