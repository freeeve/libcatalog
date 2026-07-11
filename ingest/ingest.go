package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/storage"
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
	// WorkIDs are the Works this run's records resolved to -- the presence
	// set the feed reconciliation pass diffs the corpus against.
	WorkIDs []string
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
	var seeds []MergeSeed
	if ms, ok := prov.(MergeSeeder); ok {
		seeds = ms.MergeSeeds()
	}
	works, res, r := cluster(recs, prior, seeds)

	stats, err := bibframe.BuildWorks(storage.Dir(out), works, feed)
	if err != nil {
		return res, err
	}
	res.Stats = stats

	retired, err := removeRetiredGrains(out, r.Merges())
	if err != nil {
		return res, err
	}
	res.Retired = retired
	res.Conflicts = r.Conflicts()
	return res, nil
}

// cluster is the provider-independent middle of an ingest run: it seeds the
// resolver from the prior grains (identity map, editorial merges, split
// pins), resolves every record to its stable two-tier ids, groups records
// into WorkGroups (first record wins shared metadata and per-record
// capabilities; duplicate Instances emit once), and carries each Work's
// preserved editorial statements. Shared by the directory Run and the
// store-backed RunStore.
func cluster(recs []Record, prior bibframe.Prior, mergeSeeds []MergeSeed) ([]bibframe.WorkGroup, Result, *identity.Resolver) {
	r := identity.NewResolver()
	identity.SeedResolver(r, prior.Grains)
	// Seed editorial merges and split pins: a merge resolves a retired
	// Work's Instances onto the survivor; a pin forces an over-merged Instance onto
	// its split-off Work. Neither can be undone by the computed key.
	for _, m := range prior.Merges {
		r.SeedMerge(m.From, m.To)
	}
	for _, p := range prior.Pins {
		r.SeedPin(p.Instance, p.Work)
	}
	// Seed feed cluster-merges: a source that folded one cluster into
	// another names the pair by provider id; translate each to Work ids through the
	// now-seeded resolver and merge the retired Work onto the survivor, so a
	// re-clustered record resolves to the survivor's prior grain instead of orphaning
	// one. A merge whose ids the resolver does not know (no prior grain) is skipped.
	for _, m := range mergeSeeds {
		from, okF := r.WorkForProviderKey(m.FromKey)
		to, okT := r.WorkForProviderKey(m.ToKey)
		if okF && okT && from != to {
			r.SeedMerge(from, to)
		}
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
			// `extra` object via the feed provenance graph.
			if ep, ok := rec.(ExtraProvider); ok {
				wg.Extras = ep.Extras()
			}
			// The first record likewise contributes the Work's controlled subjects
			// (authority URIs + labels + broader), emitted into the feed graph.
			if se, ok := rec.(SubjectEnricher); ok {
				wg.Subjects = se.ControlledSubjects()
			}
			// And its standalone term descriptions (ancestor chains): labels +
			// hierarchy only, no subject link.
			if td, ok := rec.(TermDescriber); ok {
				wg.Terms = td.DescribedTerms()
			}
			byWork[a.WorkID] = wg
		}
		if seenInstance[a.InstanceID] {
			continue
		}
		seenInstance[a.InstanceID] = true
		gi := bibframe.GroupInstance{
			InstanceID: a.InstanceID,
			Instance:   rec.Instance(),
		}
		// A MARC-sourced record's crosswalk-lossy fields ride along verbatim
		//, so the loss table stays a rendering concern, not a
		// data loss.
		if vp, ok := rec.(VerbatimProvider); ok {
			gi.Verbatim = vp.Verbatim()
		}
		wg.Instances = append(wg.Instances, gi)
	}

	ids := make([]string, 0, len(byWork))
	for id := range byWork {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	res.WorkIDs = ids
	works := make([]bibframe.WorkGroup, 0, len(ids))
	for _, id := range ids {
		wg := byWork[id]
		// Carry the Work's committed editorial statements across the re-ingest so the
		// feed rewrite does not clobber them (ARCHITECTURE §5).
		wg.Editorial = prior.Editorial[id]
		works = append(works, *wg)
	}
	return works, res, r
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
