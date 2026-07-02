package bibframe

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/freeeve/libcatalog/storage"
	"github.com/freeeve/libcodex/rdf"
)

// SerializeGrains regenerates the bulk catalog.nq from the committed per-Work
// grains under dir, without re-ingesting from a provider cache. It walks every
// grain (*.nq, skipping catalog.nq itself), and re-emits each grain's statements
// through one shared encoder -- so blank-node labels stay unique across the corpus
// (ARCHITECTURE §3) rather than colliding as an independently-canonicalized
// per-grain concatenation would -- in Work-id order. The grains stay the source of
// truth; catalog.nq is a derived merge of them.
//
// The output represents the same RDF as an ingest-produced catalog.nq (it projects
// identically) but is not byte-identical to it: ingest serializes each Work's
// freshly built graph, whereas this re-serializes the on-disk canonical grains, so
// blank-node labels differ. SerializeGrains is itself deterministic -- the same
// grains yield the same bytes -- so a re-serialize is a clean (empty) diff. It
// returns the number of grains merged.
func SerializeGrains(dir string, sink storage.Sink) (int, error) {
	type grain struct{ id, path string }
	var grains []grain
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".nq") || d.Name() == "catalog.nq" {
			return nil
		}
		grains = append(grains, grain{id: strings.TrimSuffix(d.Name(), ".nq"), path: path})
		return nil
	})
	if err != nil {
		return 0, err
	}
	sort.Slice(grains, func(i, j int) bool { return grains[i].id < grains[j].id })

	w, err := sink.Create("catalog.nq")
	if err != nil {
		return 0, fmt.Errorf("create catalog.nq: %w", err)
	}
	var enc rdf.Encoder
	for _, g := range grains {
		b, err := os.ReadFile(g.path)
		if err != nil {
			w.Close()
			return 0, err
		}
		ds, err := rdf.ParseNQuads(b)
		if err != nil {
			w.Close()
			return 0, fmt.Errorf("%s: %w", g.path, err)
		}
		for _, gt := range sortedGraphs(ds) {
			if _, err := w.Write(enc.AppendNQuads(nil, ds.Graph(gt), gt)); err != nil {
				w.Close()
				return 0, fmt.Errorf("write catalog.nq: %w", err)
			}
		}
	}
	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("close catalog.nq: %w", err)
	}
	return len(grains), nil
}

// sortedGraphs returns a dataset's graph terms ordered by IRI value, so a grain's
// feed and editorial statements are emitted in a fixed order regardless of parse
// order -- keeping SerializeGrains deterministic.
func sortedGraphs(ds *rdf.Dataset) []rdf.Term {
	graphs := ds.Graphs()
	sort.Slice(graphs, func(i, j int) bool { return graphs[i].Value < graphs[j].Value })
	return graphs
}
