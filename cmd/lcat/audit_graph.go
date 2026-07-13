package main

import (
	"fmt"

	chickpeas "github.com/freeeve/gochickpeas"

	"github.com/freeeve/libcat/diversity"
	"github.com/freeeve/libcat/project"
)

// auditGraph runs the content-diversity audit over a catalog.nq dataset loaded
// into an in-memory gochickpeas graph -- the FULL corpus, including
// works the public projection suppresses, which is what a collection-development
// audit should see. The chickpeas N-Quads mapping puts each bf:Work behind a
// "Work" label, each bf:subject link behind a "subject" rel, and every literal
// (skos:prefLabel, rdfs:label, the lcat extra/* passthroughs) behind a node
// property keyed by the predicate's local name, so the walk is: Work nodes ->
// subject neighbors -> (uri, prefLabel|label) pairs. The subject's vocabulary
// scheme derives from its URI namespace (project.SchemeForURI), feeding the
// crosswalk's scheme dimension exactly like the projected catalog's
// Subject.Scheme does.
func auditGraph(path string, cw *diversity.Crosswalk, filters filterFlags) (diversity.Report, error) {
	g, err := chickpeas.ReadNQuadsFile(path)
	if err != nil {
		return diversity.Report{}, fmt.Errorf("load graph: %w", err)
	}
	works, ok := g.NodesWithLabel("Work")
	if !ok {
		return diversity.Report{}, fmt.Errorf("audit: %s has no Work nodes -- is it a catalog.nq from `lcat serialize`/`lcat ingest`?", path)
	}

	// Hierarchy-aware facets: expand each category's roots through the
	// graph's own skos:broader edges (the grains carry every attached
	// term's broader chain), so `roots` needs no external fetch.
	a := diversity.NewAuditor(cw.WithNarrower(narrowerFromGraph(g)))
	for w := range works.Iter() {
		node := chickpeas.NodeID(w)
		if !filters.matchGraph(g, node) {
			continue
		}
		var refs []diversity.SubjectRef
		for s := range g.Neighbors(node, chickpeas.Outgoing, "subject") {
			uri := g.Prop(s, "uri").StrOr("")
			label := g.Prop(s, "prefLabel").StrOr("")
			if label == "" {
				label = g.Prop(s, "label").StrOr("")
			}
			ref := diversity.SubjectRef{URI: uri, Scheme: project.SchemeForURI(uri)}
			if label != "" {
				ref.Labels = []string{label}
			}
			refs = append(refs, ref)
		}
		a.Add(refs)
	}
	return a.Report(), nil
}

// narrowerFromGraph builds a parent-URI -> child-URIs resolver from the
// dataset's skos:broader edges in one pass over the nodes. The chickpeas
// N-Quads mapping turns each IRI-to-IRI triple into a rel named by the
// predicate's local name, so a term's broader link is an outgoing "broader"
// rel to its parent node.
func narrowerFromGraph(g *chickpeas.Snapshot) func(string) []string {
	children := map[string][]string{}
	for n := uint32(0); n < g.NodeCount(); n++ {
		node := chickpeas.NodeID(n)
		child := g.Prop(node, "uri").StrOr("")
		if child == "" {
			continue
		}
		for p := range g.Neighbors(node, chickpeas.Outgoing, "broader") {
			if parent := g.Prop(p, "uri").StrOr(""); parent != "" {
				children[parent] = append(children[parent], child)
			}
		}
	}
	return func(uri string) []string { return children[uri] }
}

// matchGraph is filterFlags.match against a graph work node's extra properties:
// the lcat extra/* predicates land as node props keyed by extra key, so
// --filter inQll=true reads the same value the projected catalog's extra map
// carries.
func (f filterFlags) matchGraph(g *chickpeas.Snapshot, node chickpeas.NodeID) bool {
	for _, p := range f {
		got, ok := g.Prop(node, p.key).Str()
		if !ok || !valueMatches(got, p.value) {
			return false
		}
	}
	return true
}
