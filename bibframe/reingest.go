package bibframe

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/freeeve/libcat/identity"
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
	Merges    []identity.Merge  // editorial lcat:mergedInto decisions to seed
	Pins      []identity.Pin    // editorial lcat:workAssignment split pins to seed
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
		if d.IsDir() || !isWorkGrainName(d.Name()) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := prior.accumulateGrain(b, feed); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	})
	return prior, err
}

// isWorkGrainName reports whether a file basename is a per-Work grain: *.nq,
// excluding the bulk catalog.nq -- the one skip rule both prior loaders
// share.
func isWorkGrainName(base string) bool {
	return strings.HasSuffix(base, ".nq") && base != "catalog.nq"
}

// accumulateGrain scans one grain into the prior off a single parse: its
// committed identity, its preserved non-feed statements per Work, and its
// editorial merge/pin decisions -- the shared per-grain core of LoadPrior
// (filesystem) and LoadPriorStore (blob store).
func (p *Prior) accumulateGrain(grain []byte, feed rdf.Term) error {
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return err
	}
	gi := identity.ScanDataset(ds)
	p.Grains = append(p.Grains, gi)
	ed := preservedQuads(ds, feed)
	if len(ed) > 0 {
		// Namespace this grain's preserved blanks: after a merge, two grains'
		// preserved statements share one Editorial buffer, and each grain's
		// labels count from the same seed and would fuse. The prefix survives
		// only until the next joint canonicalization.
		ed = RelabelGrainBlanks(ed, fmt.Sprintf("g%d_", len(p.Grains)-1))
	}
	for _, wk := range gi.Works {
		if len(ed) > 0 {
			p.Editorial[wk.WorkID] = append(p.Editorial[wk.WorkID], ed...)
		}
	}
	p.Merges = append(p.Merges, ScanMergesDataset(ds)...)
	p.Pins = append(p.Pins, scanPinsDataset(ds)...)
	return nil
}

// preservedQuads returns the raw N-Quads of every provenance graph in the grain
// other than feed -- the editorial (and any future non-feed) statements to carry
// across re-ingest (ARCHITECTURE §5).
func preservedQuads(ds *rdf.Dataset, feed rdf.Term) []byte {
	// One Encoder across every preserved graph: a fresh encoder per graph
	// would renumber each graph's blanks from _:b1 and fuse unrelated nodes
	// when the outputs concatenate (the Encoder's own doc warns
	// exactly this). Statement order interleaves by input order, which the
	// eventual joint canonicalization erases.
	var e rdf.Encoder
	var out []byte
	for _, q := range ds.Quads {
		if q.G != feed {
			out = e.AppendQuad(out, q)
		}
	}
	return out
}
