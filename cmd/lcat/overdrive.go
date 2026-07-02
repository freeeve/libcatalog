package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/identity"
	"github.com/freeeve/libcatalog/ingest/overdrive"
	"github.com/freeeve/libcatalog/storage"
	"github.com/freeeve/libcodex/iso2709"
)

// runOverdrive ingests a cached OverDrive scan (page-*.json). With --out it maps
// the Thunder JSON directly to canonical BIBFRAME grains with stable, minted
// two-tier ids (ARCHITECTURE §4/§9): any grains already under --out seed the
// resolver, so re-ingest reuses ids and clusters editions into one Work. With
// --marc it also exports an ISO 2709 fixture for the MARC-import ramp (tasks/007).
func runOverdrive(args []string) error {
	fs := flag.NewFlagSet("overdrive", flag.ExitOnError)
	cache := fs.String("cache", "", "OverDrive page-cache directory (contains page-*.json)")
	out := fs.String("out", "", "output directory for canonical grains (direct JSON->BIBFRAME)")
	marcOut := fs.String("marc", "", "optional MARC (.mrc) fixture output (the MARC-import ramp)")
	provider := fs.String("provider", "overdrive", "provenance graph feed:<provider> for the records")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cache == "" {
		return fmt.Errorf("--cache is required")
	}
	if *out == "" && *marcOut == "" {
		return fmt.Errorf("one of --out (grains) or --marc (fixture) is required")
	}

	items, err := overdrive.ReadCache(*cache)
	if err != nil {
		return err
	}

	if *marcOut != "" {
		if err := writeOverdriveMARC(items, *marcOut); err != nil {
			return err
		}
	}
	if *out != "" {
		if err := buildOverdriveGrains(items, *out, *provider); err != nil {
			return err
		}
	}
	return nil
}

// buildOverdriveGrains resolves each item to stable Work/Instance ids (seeding
// the resolver from any grains already under out), clusters items into Works, and
// writes one per-Work grain.
func buildOverdriveGrains(items []overdrive.Item, out, provider string) error {
	prior, err := bibframe.LoadPrior(out, provider)
	if err != nil {
		return fmt.Errorf("load prior grains: %w", err)
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

	// Group items by resolved Work. The first item to reach a Work supplies its
	// shared Work-level metadata, and the first to reach an Instance supplies that
	// Instance; iterating in cache (page) order makes both deterministic across
	// runs. Later items that resolve onto an already-seen Instance (e.g. a shared
	// ISBN) are duplicates and skipped, so each Instance node is emitted once.
	byWork := map[string]*bibframe.WorkGroup{}
	seenInstance := map[string]bool{}
	var mintedWorks, mintedInstances int
	for _, it := range items {
		a := r.Resolve(it.Identity())
		if a.MintedWork {
			mintedWorks++
		}
		if a.MintedInstance {
			mintedInstances++
		}
		wg, ok := byWork[a.WorkID]
		if !ok {
			wg = &bibframe.WorkGroup{WorkID: a.WorkID, Work: it.Work()}
			byWork[a.WorkID] = wg
		}
		if seenInstance[a.InstanceID] {
			continue
		}
		seenInstance[a.InstanceID] = true
		wg.Instances = append(wg.Instances, bibframe.GroupInstance{
			InstanceID: a.InstanceID,
			Instance:   it.Instance(),
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
		// Carry the Work's committed editorial statements across the re-ingest so
		// the feed rewrite does not clobber them (ARCHITECTURE §5).
		wg.Editorial = prior.Editorial[id]
		works = append(works, *wg)
	}

	stats, err := bibframe.BuildWorks(storage.Dir(out), works, provider)
	if err != nil {
		return err
	}
	// Drop the grains of Works retired by an editorial merge: their Instances have
	// just been rewritten onto the survivor, so the stale grain would otherwise
	// re-seed the retired id as a live cluster on the next ingest (tasks/001).
	retired, err := removeRetiredGrains(out, prior.Merges)
	if err != nil {
		return err
	}
	for _, c := range r.Conflicts() {
		fmt.Fprintln(os.Stderr, "lcat overdrive: conflict:", c)
	}
	fmt.Printf("built %d works from %d instances under %s (feed:%s); minted %d works, %d instances; retired %d works\n",
		stats.Grains, stats.Records, out, provider, mintedWorks, mintedInstances, retired)
	return nil
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

// writeOverdriveMARC exports the cached items as an ISO 2709 MARC file.
func writeOverdriveMARC(items []overdrive.Item, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := iso2709.NewWriter(f)
	for _, rec := range overdrive.Records(items) {
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	fmt.Printf("wrote %d records to %s\n", len(items), path)
	return nil
}
