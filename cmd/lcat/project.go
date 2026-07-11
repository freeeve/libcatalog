package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/freeeve/libcat/project"
)

// runProject reads a catalog.nq dataset and writes the projected catalog.json --
// the derived data the Hugo module's content adapter and the search index consume
// (ARCHITECTURE §7). The graph stays the source of truth; this is a build artifact.
//
// --provider takes a comma-separated feed list (tasks/172): the projector views
// one feed graph at a time, so each feed projects separately and the catalogs
// merge by work id, first-listed feed winning a shared work. After a multi-feed
// ingest run `lcat serialize` first, since each ingest run rewrites catalog.nq
// with only its own run's works.
func runProject(args []string) error {
	fs := flag.NewFlagSet("project", flag.ExitOnError)
	catalogNQ := fs.String("catalog", "", "path to a catalog.nq dataset")
	out := fs.String("out", ".", "output directory for catalog.json")
	provider := fs.String("provider", "overdrive", "provenance graph feed(s) to project, comma-separated, first wins")
	publicSources := fs.String("public-sources", "",
		"comma-separated extra.sources names allowed on the public face; others are stripped. Empty (default) keeps everything.")
	publicExtras := fs.String("public-extras", "",
		"comma-separated extra keys allowed on the public face; others are dropped from catalog.json. `sources` is governed by --public-sources instead. Empty (default) keeps everything.")
	schemeMap := fs.String("subject-scheme", "",
		"extra authority namespace -> scheme entries, comma-separated prefix=code pairs (prepended, so they override the built-in table)")
	allowEmpty := fs.Bool("allow-empty", false,
		"write a catalog with zero works instead of failing (a fresh deployment before its first ingest)")
	similarLimit := fs.Int("similar", DefaultSimilarLimit,
		"neighbours per work in the similar.json \"more like this\" sidecar; 0 skips it entirely")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogNQ == "" {
		return fmt.Errorf("--catalog is required")
	}
	if err := applySubjectSchemes(splitList(*schemeMap)); err != nil {
		return err
	}
	return projectCatalog(projectOptions{
		CatalogPath:   *catalogNQ,
		Providers:     splitList(*provider),
		PublicSources: splitList(*publicSources),
		PublicExtras:  splitList(*publicExtras),
		Out:           *out,
		AllowEmpty:    *allowEmpty,
		SimilarLimit:  *similarLimit,
	})
}

// DefaultSimilarLimit is how many neighbours each Work's rail carries. qllpoc
// reveals 8 at a time from a deeper pool; a static page has no "show more", so 8
// is the whole rail.
const DefaultSimilarLimit = 8

// applySubjectSchemes prepends deployment prefix=code entries to the
// namespace -> scheme table, so they override the built-ins (tasks/141).
func applySubjectSchemes(pairs []string) error {
	if len(pairs) == 0 {
		return nil
	}
	var extra []project.SchemePrefix
	for _, pair := range pairs {
		prefix, code, ok := strings.Cut(pair, "=")
		if !ok || prefix == "" || code == "" {
			return fmt.Errorf("bad subject-scheme entry %q (want prefix=code)", pair)
		}
		extra = append(extra, project.SchemePrefix{Prefix: prefix, Scheme: code})
	}
	project.SubjectSchemePrefixes = append(extra, project.SubjectSchemePrefixes...)
	return nil
}

// projectCatalog is the projection step shared by `lcat project` and `lcat
// build`: it projects each named feed from the catalog.nq at catalogPath,
// merges first-feed-wins, applies the public-sources allowlist when one is
// given, and writes catalog.json + facets.json + redirects.json + similar.json
// to out.
// projectOptions is what one projection run needs. A struct rather than seven
// positional parameters: two of them are adjacent []string allowlists whose
// meanings differ (source names vs extra keys), and nothing would have caught
// them being swapped (tasks/277).
type projectOptions struct {
	CatalogPath string
	Providers   []string
	// PublicSources allowlists extra.sources attributions (tasks/172).
	PublicSources []string
	// PublicExtras allowlists extra keys (tasks/277). "sources" is exempt.
	PublicExtras []string
	Out          string
	AllowEmpty   bool
	SimilarLimit int
}

func projectCatalog(opts projectOptions) error {
	catalogPath, providers, out := opts.CatalogPath, opts.Providers, opts.Out
	allowEmpty, similarLimit := opts.AllowEmpty, opts.SimilarLimit
	// One streaming load for every feed, the feed list and the redirects. Each of
	// those used to reparse the whole catalog -- five full parses of a 1.76M-quad
	// corpus on a three-feed deployment -- and each parse retained the authority
	// snapshots the projection reads three predicates of (tasks/279).
	ds, err := project.LoadDataset(catalogPath)
	if err != nil {
		return err
	}
	var cats []*project.Catalog
	for _, p := range providers {
		cats = append(cats, project.ProjectDataset(ds, p))
	}
	if len(cats) == 0 {
		return fmt.Errorf("no feeds to project")
	}
	present := project.FeedsDataset(ds)
	warnMissingFeeds(providers, present)
	cat := project.Merge(cats)
	// Refuse to publish an empty catalog over a populated one. This function is
	// what LCATD_REBUILD_CMD runs on every publish, so a provider that names no
	// feed -- a typo, a renamed feed -- would otherwise overwrite catalog.json
	// with zero works, exit 0, and quietly empty the discovery site (tasks/246).
	if len(cat.Works) == 0 && !allowEmpty {
		return emptyProjectionError(providers, present)
	}
	// Order matters: SanitizeSources filters *within* the sources value, and
	// SanitizeExtras drops private keys but never touches `sources`. Running
	// extras first would still be correct, but this reads as the two allowlists
	// answering the two questions in the order the config lists them.
	if len(opts.PublicSources) > 0 {
		allow := project.SourceSet(strings.Join(opts.PublicSources, ","))
		if stripped := project.SanitizeSources(cat, allow); stripped > 0 {
			fmt.Fprintf(os.Stderr, "project: stripped %d private source attributions from the public catalog\n", stripped)
		}
	}
	if len(opts.PublicExtras) > 0 {
		allow := project.SourceSet(strings.Join(opts.PublicExtras, ","))
		if stripped := project.SanitizeExtras(cat, allow); stripped > 0 {
			fmt.Fprintf(os.Stderr, "project: stripped %d private extra values from the public catalog\n", stripped)
		}
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "catalog.json"), cat); err != nil {
		return err
	}
	facets := cat.Facets()
	if err := writeJSON(filepath.Join(out, "facets.json"), facets); err != nil {
		return err
	}
	redirects := project.RedirectsDataset(ds)
	if err := writeJSON(filepath.Join(out, "redirects.json"), redirects); err != nil {
		return err
	}
	fmt.Printf("projected %d works to %s (schema v%d); facets: %d languages, %d subjects, %d contributors; %d redirects\n",
		len(cat.Works), out, project.SchemaVersion,
		len(facets.Languages), len(facets.Subjects), len(facets.Contributors), len(redirects.Redirects))
	return writeSimilar(cat, out, similarLimit)
}

// writeSimilar computes and writes the "more like this" sidecar (tasks/284).
//
// It reports how many Works ended up with no neighbours at all rather than only
// how many did: on a catalog whose subjects are thin, a rail that is empty
// everywhere is the useful signal, and a silent success would hide it.
func writeSimilar(cat *project.Catalog, out string, limit int) error {
	path := filepath.Join(out, "similar.json")
	if limit <= 0 {
		// Turning the rail off has to remove the sidecar, not merely stop
		// rewriting it: the Hugo module renders whatever similar.json it finds,
		// so a stale one from the previous projection would keep serving
		// neighbours for works that may no longer exist.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		fmt.Println("similar: skipped (--similar=0); the OPAC renders no \"more like this\" rail")
		return nil
	}
	start := time.Now()
	idx := cat.Similar(limit)
	if err := writeJSON(path, idx); err != nil {
		return err
	}
	withNone := len(cat.Works) - len(idx.Works)
	fmt.Printf("similar: %d of %d works have neighbours (<=%d each) in %s\n",
		len(idx.Works), len(cat.Works), limit, time.Since(start).Round(time.Millisecond))
	if withNone > 0 {
		fmt.Fprintf(os.Stderr, "similar: %d works have no neighbours -- they share no subject, tag, contributor or series with any other work\n", withNone)
	}
	return nil
}

// warnMissingFeeds names requested providers the catalog does not carry. A
// missing feed among several contributes nothing to the merge, which is easy to
// miss when the others still project works.
func warnMissingFeeds(providers, present []string) {
	have := make(map[string]bool, len(present))
	for _, p := range present {
		have[p] = true
	}
	var missing []string
	for _, p := range providers {
		if !have[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 && len(missing) < len(providers) {
		fmt.Fprintf(os.Stderr, "project: no feed named %s in the catalog (it carries %s); projecting the rest\n",
			strings.Join(missing, ", "), strings.Join(present, ", "))
	}
}

// emptyProjectionError explains why nothing projected, distinguishing a catalog
// whose feeds were not the ones asked for from one that carries no feeds at all.
func emptyProjectionError(providers, present []string) error {
	if len(present) == 0 {
		return fmt.Errorf("projected 0 works: the catalog carries no feed graphs at all. "+
			"Pass --allow-empty if projecting an empty catalog is intended (provider %s)",
			strings.Join(providers, ", "))
	}
	return fmt.Errorf("projected 0 works: --provider %s matches no feed in the catalog, which carries %s. "+
		"Refusing to overwrite %s with an empty catalog; pass --allow-empty to do it anyway",
		strings.Join(providers, ", "), strings.Join(present, ", "), "catalog.json")
}

// writeJSON marshals v as indented JSON to path.
func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
