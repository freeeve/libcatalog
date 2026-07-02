package bibframe

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/freeeve/libcatalog/identity"
	"github.com/freeeve/libcodex/rdf"
)

// Prior is the committed state a re-ingest recovers from the grains under a build
// directory: the identity to seed the resolver (so ids do not churn) and the
// editorial statements to preserve per Work (so a feed re-ingest is clobber-safe).
// It is the read side of the derive-from-grains model (ARCHITECTURE §4/§5,
// Decision A): the grains are the durable identity map and the editorial store.
type Prior struct {
	Grains    []identity.GrainIdentity
	Editorial map[string][]byte // Work id -> raw N-Quads of its non-feed statements
}

// LoadPrior reads every per-Work grain (*.nq, skipping the bulk catalog.nq) under
// dir, returning the recovered identity and the preserved editorial statements
// keyed by Work id. A missing directory (a first build) yields empty state and no
// error.
func LoadPrior(dir, provider string) (Prior, error) {
	prior := Prior{Editorial: map[string][]byte{}}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return prior, nil
	}
	feed := FeedGraph(provider)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".nq") || d.Name() == "catalog.nq" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		gi, err := identity.ScanGrain(b)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		prior.Grains = append(prior.Grains, gi)
		ed, err := preservedQuads(b, feed)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		for _, wk := range gi.Works {
			if len(ed) > 0 {
				prior.Editorial[wk.WorkID] = append(prior.Editorial[wk.WorkID], ed...)
			}
		}
		return nil
	})
	return prior, err
}

// preservedQuads returns the raw N-Quads of every provenance graph in the grain
// other than feed -- the editorial (and any future non-feed) statements to carry
// across re-ingest (ARCHITECTURE §5).
func preservedQuads(grain []byte, feed rdf.Term) ([]byte, error) {
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, gt := range ds.Graphs() {
		if gt == feed {
			continue
		}
		out = append(out, ds.Graph(gt).NQuads(gt)...)
	}
	return out, nil
}
