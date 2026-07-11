package project

import (
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/freeeve/libcat/similar"
)

// SimilarSchemaVersion is similar.json's own schema version.
//
// It is deliberately not SchemaVersion. That number exists so a consumer can
// detect a projector/consumer mismatch in catalog.json, facets.json and
// redirects.json -- artifacts the Hugo module cannot render without. similar.json
// is optional on both sides: a module built before it ignores the file, and a
// module built after it renders no rail when the file is absent. Bumping
// SchemaVersion would force every adopter into a lockstep reproject to announce a
// mismatch that cannot occur.
const SimilarSchemaVersion = 1

// SimilarNeighbor is one recommended Work: enough to render a card without a
// second lookup, plus the shared attributes that answer the only question a
// librarian asks about a recommendation, which is "why is this here?".
type SimilarNeighbor struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Shared lists the attribute values that put this Work on the list, most
	// significant first, capped by the scorer. A neighbour reached only through
	// the concept tree or a flat bonus can legitimately share nothing verbatim.
	Shared []string `json:"shared,omitempty"`
}

// SimilarIndex is the similar.json sidecar: for each Work, its ranked neighbours.
// Works with no neighbours are omitted rather than stored empty.
type SimilarIndex struct {
	Version int `json:"version"`
	// Limit is how many neighbours were kept per Work, so a consumer can tell a
	// short rail from a truncated one.
	Limit int                          `json:"limit"`
	Works map[string][]SimilarNeighbor `json:"works"`
}

// SimilarWork converts a projected Work into the similarity scorer's input
// . It is one of exactly two converters -- ingest.WorkSummary.SimilarWork
// is the other -- and a test drives both from the same graph and requires equal
// results, so the OPAC's precomputed rail and the admin's live panel cannot drift.
//
// Series is transcribed per Instance and hoisted to the Work, matching what the
// admin summary does. Tombstoned is always false: the projection drops retired
// Works upstream and never sees one.
func (w Work) SimilarWork() similar.Work {
	names := make([]string, 0, len(w.Contributors))
	for _, c := range w.Contributors {
		names = append(names, c.Name)
	}
	subjects := make([]string, 0, len(w.Subjects))
	for _, s := range w.Subjects {
		subjects = append(subjects, s.ID)
	}
	// Series are Work-level since so the scorer no longer collects them
	// across Instances and de-duplicates. The *titles* are what links two Works:
	// "bk. 2" and "bk. 7" of one series are neighbours, and an enumeration shared
	// by two unrelated series is a coincidence.
	series := make([]string, 0, len(w.Series))
	for _, s := range w.Series {
		series = append(series, s.Title)
	}
	return similar.Work{
		WorkID:       w.ID,
		Held:         w.Held,
		Series:       series,
		Contributors: names,
		Tags:         w.Tags,
		Subjects:     subjects,
		Languages:    w.Languages,
	}
}

// SimilarWorks converts the catalog, preserving order.
func (c *Catalog) SimilarWorks() []similar.Work {
	out := make([]similar.Work, 0, len(c.Works))
	for _, w := range c.Works {
		out = append(out, w.SimilarWork())
	}
	return out
}

// conceptTree reads the SKOS hierarchy out of the catalog itself -- the Terms
// sideband (schema v10) carries every referenced term plus its transitive
// broader ancestors -- so the projector walks the concept tree without importing
// backend/vocab or re-reading the graph. Subject.Broader on a Work contributes
// the same edges for a term the sideband happened to skip.
func (c *Catalog) conceptTree() (broader, narrower func(string) []string) {
	up := map[string][]string{}
	add := func(id string, parents []string) {
		if len(parents) == 0 {
			return
		}
		seen := map[string]bool{}
		for _, p := range up[id] {
			seen[p] = true
		}
		for _, p := range parents {
			if p != "" && p != id && !seen[p] {
				seen[p] = true
				up[id] = append(up[id], p)
			}
		}
	}
	for _, t := range c.Terms {
		add(t.ID, t.Broader)
	}
	for _, w := range c.Works {
		for _, s := range w.Subjects {
			add(s.ID, s.Broader)
		}
	}
	down := map[string][]string{}
	for child, parents := range up {
		for _, p := range parents {
			down[p] = append(down[p], child)
		}
	}
	return func(id string) []string { return up[id] }, func(id string) []string { return down[id] }
}

// SimilarOptions is the scorer configuration the projector uses: the defaults,
// wired to the catalog's own concept tree.
func (c *Catalog) SimilarOptions() similar.Options {
	opts := similar.DefaultOptions()
	opts.Broader, opts.Narrower = c.conceptTree()
	return opts
}

// Similar computes each Work's neighbours and returns the similar.json sidecar.
// limit <= 0 yields an empty index rather than an unbounded one.
//
// The pass is parallel across Works. Neighbors is read-only on a built Index, so
// this is embarrassingly parallel, and it needs to be: at 62,602 Works a serial
// pass is ~84 s and ~43 GB of allocation churn, on top of a projector that
// already peaks near 2 GB. Build itself stays serial -- it is 44 ms.
func (c *Catalog) Similar(limit int) *SimilarIndex {
	idx := &SimilarIndex{Version: SimilarSchemaVersion, Limit: limit, Works: map[string][]SimilarNeighbor{}}
	if limit <= 0 || len(c.Works) == 0 {
		return idx
	}
	ix := similar.Build(c.SimilarWorks(), c.SimilarOptions())

	titles := make(map[string]string, len(c.Works))
	for _, w := range c.Works {
		titles[w.ID] = w.Title
	}

	// Results land in a slice indexed by Work offset, never a shared map, so the
	// sidecar is byte-identical whatever order the workers finish in.
	rows := make([][]SimilarNeighbor, len(c.Works))
	var next atomic.Int64
	var wg sync.WaitGroup
	workers := min(runtime.NumCPU(), len(c.Works))
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(c.Works) {
					return
				}
				scored := ix.Neighbors(c.Works[i].ID, limit)
				if len(scored) == 0 {
					continue
				}
				row := make([]SimilarNeighbor, 0, len(scored))
				for _, s := range scored {
					row = append(row, SimilarNeighbor{ID: s.WorkID, Title: titles[s.WorkID], Shared: s.Shared})
				}
				rows[i] = row
			}
		}()
	}
	wg.Wait()

	for i, row := range rows {
		if len(row) > 0 {
			idx.Works[c.Works[i].ID] = row
		}
	}
	return idx
}
