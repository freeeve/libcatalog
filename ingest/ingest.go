package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/identity"
	"github.com/freeeve/libcatalog/storage"
)

// Result is what one ingest run produced: the grain/instance counts, how many ids
// were freshly minted at each tier, how many merge-retired grains were dropped, and
// any resolver conflicts to surface. The pipeline returns this rather than printing
// so the CLI (or a cloud handler) owns presentation.
type Result struct {
	Stats           bibframe.BuildStats
	MintedWorks     int
	MintedInstances int
	Retired         int
	Conflicts       []string
}

// Run ingests a provider's records into canonical grains under out, the shared
// direct-BIBFRAME pipeline every ingest provider uses (ARCHITECTURE §4/§9). It
// seeds the resolver from any grains already under out (so re-ingest reuses ids and
// clusters editions), applies committed editorial merges/pins, resolves each record
// to stable two-tier ids, groups records into Works (first record wins shared
// metadata; a duplicate Instance -- e.g. a shared ISBN -- is emitted once), carries
// each Work's preserved editorial statements across the feed rewrite, writes one
// grain per Work plus catalog.nq, and drops the grains of merge-retired Works. The
// run is deterministic in record order. Only ingest-role providers are executed.
func Run(prov Provider, out string) (Result, error) {
	if prov.Role() != RoleIngest {
		return Result{}, fmt.Errorf("ingest: provider %q has role %s, not ingest", prov.Name(), prov.Role())
	}
	feed := prov.Name()

	recs, err := prov.Records(context.Background())
	if err != nil {
		return Result{}, fmt.Errorf("provider %q records: %w", feed, err)
	}

	prior, err := bibframe.LoadPrior(out, feed)
	if err != nil {
		return Result{}, fmt.Errorf("load prior grains: %w", err)
	}
	r := identity.NewResolver()
	identity.SeedResolver(r, prior.Grains)
	// Seed editorial merges and split pins (tasks/001): a merge resolves a retired
	// Work's Instances onto the survivor; a pin forces an over-merged Instance onto
	// its split-off Work. Neither can be undone by the computed key.
	for _, m := range prior.Merges {
		r.SeedMerge(m.From, m.To)
	}
	for _, p := range prior.Pins {
		r.SeedPin(p.Instance, p.Work)
	}

	byWork := map[string]*bibframe.WorkGroup{}
	seenInstance := map[string]bool{}
	var res Result
	for _, rec := range recs {
		a := r.Resolve(rec.Identity())
		if a.MintedWork {
			res.MintedWorks++
		}
		if a.MintedInstance {
			res.MintedInstances++
		}
		wg, ok := byWork[a.WorkID]
		if !ok {
			wg = &bibframe.WorkGroup{WorkID: a.WorkID, Work: rec.Work()}
			// The first record of a clustered Work also supplies its non-BIBFRAME
			// display extras (cover/rating/dateRead), carried through to catalog.json's
			// `extra` object via the feed provenance graph (tasks/026).
			if ep, ok := rec.(ExtraProvider); ok {
				wg.Extras = ep.Extras()
			}
			byWork[a.WorkID] = wg
		}
		if seenInstance[a.InstanceID] {
			continue
		}
		seenInstance[a.InstanceID] = true
		wg.Instances = append(wg.Instances, bibframe.GroupInstance{
			InstanceID: a.InstanceID,
			Instance:   rec.Instance(),
		})
	}

	ids := make([]string, 0, len(byWork))
	for id := range byWork {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	works := make([]bibframe.WorkGroup, 0, len(ids))
	for _, id := range ids {
		wg := byWork[id]
		// Carry the Work's committed editorial statements across the re-ingest so the
		// feed rewrite does not clobber them (ARCHITECTURE §5).
		wg.Editorial = prior.Editorial[id]
		works = append(works, *wg)
	}

	stats, err := bibframe.BuildWorks(storage.Dir(out), works, feed)
	if err != nil {
		return res, err
	}
	res.Stats = stats

	retired, err := removeRetiredGrains(out, prior.Merges)
	if err != nil {
		return res, err
	}
	res.Retired = retired
	res.Conflicts = r.Conflicts()
	return res, nil
}

// removeRetiredGrains deletes the per-Work grain file of every Work retired by a
// merge and returns how many were removed. A retired grain that is already gone (a
// re-ingest after the first merge-aware run) is not an error.
func removeRetiredGrains(dir string, merges []identity.Merge) (int, error) {
	n := 0
	for _, id := range bibframe.RetiredWorks(merges) {
		path := filepath.Join(dir, filepath.FromSlash(bibframe.GrainPath(id)))
		if err := os.Remove(path); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return n, fmt.Errorf("remove retired grain %s: %w", id, err)
		}
		n++
	}
	return n, nil
}
