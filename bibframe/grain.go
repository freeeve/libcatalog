// Package bibframe crosswalks codex records into canonical BIBFRAME N-Quads
// grains. It wraps libcodex's record<->BIBFRAME conversion
// (github.com/freeeve/libcodex/bibframe) and adds libcatalog's provenance
// graphs plus RDFC-1.0 canonical emission, so each grain re-serializes
// byte-for-byte when unchanged -- the clean-diff invariant in ARCHITECTURE §3.
package bibframe

import (
	"fmt"

	codex "github.com/freeeve/libcodex"
	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/rdf"
)

// FeedGraph returns the named-graph term for a provider feed's statements: the
// feed:<provider> provenance graph from ARCHITECTURE §5. Feed statements are
// regenerated on ingest and never hand-edited.
func FeedGraph(provider string) rdf.Term {
	return rdf.NewIRI("feed:" + provider)
}

// EditorialGraph is the named graph for human- and authority-owned statements
// (ARCHITECTURE §5): merge/split decisions, curated subjects, and any hand-
// authored triples. Unlike feed:<provider> statements -- regenerated on every
// ingest -- editorial: statements are preserved verbatim across re-ingest.
func EditorialGraph() rdf.Term {
	return rdf.NewIRI("editorial:")
}

// Grain crosswalks one record to BIBFRAME and returns its canonical N-Quads
// grain: every statement tagged with the given provenance graph and RDFC-1.0
// canonicalized (blank-node labels + statement order), so an unchanged record
// re-serializes to identical bytes.
func Grain(rec *codex.Record, graph rdf.Term) ([]byte, error) {
	nq, err := codexbf.EncodeNQuads(rec, graph)
	if err != nil {
		return nil, fmt.Errorf("crosswalk record to n-quads: %w", err)
	}
	ds, err := rdf.ParseNQuads(nq)
	if err != nil {
		return nil, fmt.Errorf("parse n-quads: %w", err)
	}
	return ds.Canonical()
}
