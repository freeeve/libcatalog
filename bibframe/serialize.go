package bibframe

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/freeeve/libcat/storage"
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
// freshly built graph, whereas this merges the on-disk canonical grains, so
// blank-node labels differ. It returns the number of grains merged.
//
// A grain's bytes appear in the merge unchanged except for its blank-node labels,
// which are namespaced by grain id (tasks/291). So a grain contributes the same
// lines whether it is merged alone or with sixty thousand others, and the merged
// document changes only when a grain does -- which is what lets a published
// catalog.nq.gz keep its sha256 across a release that changed no data.
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
	// Blank-node labels are namespaced by grain id (tasks/291), so two grains
	// sharing an id would silently merge their blank nodes into one -- a wrong
	// graph, quietly. Ids are file basenames and unique in practice (work ids,
	// vocabulary schemes); say so out loud if that ever stops being true.
	for i := 1; i < len(grains); i++ {
		if grains[i].id == grains[i-1].id {
			return 0, fmt.Errorf("two grains share the id %q (%s and %s): their blank nodes would merge",
				grains[i].id, grains[i-1].path, grains[i].path)
		}
	}

	w, err := sink.Create("catalog.nq")
	if err != nil {
		return 0, fmt.Errorf("create catalog.nq: %w", err)
	}
	for _, g := range grains {
		b, err := os.ReadFile(g.path)
		if err != nil {
			w.Close()
			return 0, err
		}
		// Parse to reject a grain no parser will accept -- the error contract this
		// has always had -- but emit the grain's own bytes, not a re-serialization.
		// A grain is already canonical N-Quads; re-encoding it is a second chance
		// to differ, and the difference was a new blank-node label on every release
		// for a corpus that had not changed (tasks/291).
		if _, err := rdf.ParseNQuads(b); err != nil {
			w.Close()
			return 0, fmt.Errorf("%s: %w", g.path, err)
		}
		out := RelabelGrainBlanks(b, GrainBlankPrefix(g.path))
		if len(out) > 0 && out[len(out)-1] != '\n' {
			out = append(out, '\n')
		}
		if _, err := w.Write(out); err != nil {
			w.Close()
			return 0, fmt.Errorf("write catalog.nq: %w", err)
		}
	}
	if err := w.Close(); err != nil {
		return 0, fmt.Errorf("close catalog.nq: %w", err)
	}
	return len(grains), nil
}
