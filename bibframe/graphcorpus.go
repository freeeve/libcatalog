package bibframe

import (
	"fmt"
	"sort"

	"github.com/freeeve/libcatalog/storage"
	codexbf "github.com/freeeve/libcodex/bibframe"
	"github.com/freeeve/libcodex/rdf"
)

// Entry is one unit of a corpus built from a directly-constructed BIBFRAME --
// the path a native provider (e.g. OverDrive's JSON feed) takes instead of MARC.
// ID names the grain file and orders the catalog; Base is the IRI stem for the
// #<Base>Work / #<Base>Instance node URIs; Bib is the Work/Instance pair.
type Entry struct {
	ID   string
	Base string
	Bib  *codexbf.BIBFRAME
}

// GrainFromGraph canonicalizes one BIBFRAME graph into its N-Quads grain, every
// statement tagged with the given provenance graph and RDFC-1.0 canonicalized so
// an unchanged input re-serializes to identical bytes. It is the directly-built
// counterpart of Grain (which starts from a codex.Record).
func GrainFromGraph(g *rdf.Graph, graph rdf.Term) ([]byte, error) {
	ds, err := rdf.ParseNQuads(g.NQuads(graph))
	if err != nil {
		return nil, fmt.Errorf("parse n-quads: %w", err)
	}
	return ds.Canonical()
}

// BuildEntries writes one canonical N-Quads grain per entry into sink (at
// GrainPath) in the provider's feed graph, plus a bulk catalog.nq. It is the
// direct-BIBFRAME analogue of BuildCorpus: same grain layout and storage
// abstraction, but the graph comes straight from the provider's BIBFRAME rather
// than from a MARC record, so no data is lost to a MARC round-trip.
func BuildEntries(sink storage.Sink, entries []Entry, provider string) (BuildStats, error) {
	feed := FeedGraph(provider)
	stats := BuildStats{Records: len(entries)}

	type built struct {
		id string
		g  *rdf.Graph
	}
	graphs := make([]built, 0, len(entries))
	for _, e := range entries {
		g := e.Bib.Graph(e.Base)
		grain, err := GrainFromGraph(g, feed)
		if err != nil {
			return stats, fmt.Errorf("grain %s: %w", e.ID, err)
		}
		if err := writeSink(sink, GrainPath(e.ID), grain); err != nil {
			return stats, err
		}
		stats.Grains++
		graphs = append(graphs, built{e.ID, g})
	}

	sort.Slice(graphs, func(i, j int) bool { return graphs[i].id < graphs[j].id })
	w, err := sink.Create("catalog.nq")
	if err != nil {
		return stats, fmt.Errorf("create catalog.nq: %w", err)
	}
	// One shared encoder across the corpus keeps blank-node labels unique, so the
	// bulk file is a valid merge of the grains rather than a collision-prone
	// concatenation (ARCHITECTURE §3).
	var enc rdf.Encoder
	for _, b := range graphs {
		if _, err := w.Write(enc.AppendNQuads(nil, b.g, feed)); err != nil {
			w.Close()
			return stats, fmt.Errorf("write catalog.nq: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return stats, fmt.Errorf("close catalog.nq: %w", err)
	}
	return stats, nil
}
