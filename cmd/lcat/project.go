package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
		"comma-separated extra.sources names allowed on the public face; others are stripped (tasks/172). Empty (default) keeps everything.")
	schemeMap := fs.String("subject-scheme", "",
		"extra authority namespace -> scheme entries, comma-separated prefix=code pairs (prepended, so they override the built-in table; tasks/141)")
	allowEmpty := fs.Bool("allow-empty", false,
		"write a catalog with zero works instead of failing (a fresh deployment before its first ingest; tasks/246)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogNQ == "" {
		return fmt.Errorf("--catalog is required")
	}
	if err := applySubjectSchemes(splitList(*schemeMap)); err != nil {
		return err
	}
	return projectCatalog(*catalogNQ, splitList(*provider), splitList(*publicSources), *out, *allowEmpty)
}

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
// given, and writes catalog.json + facets.json + redirects.json to out.
func projectCatalog(catalogPath string, providers, publicSources []string, out string, allowEmpty bool) error {
	b, err := os.ReadFile(catalogPath)
	if err != nil {
		return err
	}
	var cats []*project.Catalog
	for _, p := range providers {
		c, err := project.Project(b, p)
		if err != nil {
			return fmt.Errorf("project feed %q: %w", p, err)
		}
		cats = append(cats, c)
	}
	if len(cats) == 0 {
		return fmt.Errorf("no feeds to project")
	}
	present, err := project.Feeds(b)
	if err != nil {
		return err
	}
	warnMissingFeeds(providers, present)
	cat := project.Merge(cats)
	// Refuse to publish an empty catalog over a populated one. This function is
	// what LCATD_REBUILD_CMD runs on every publish, so a provider that names no
	// feed -- a typo, a renamed feed -- would otherwise overwrite catalog.json
	// with zero works, exit 0, and quietly empty the discovery site (tasks/246).
	if len(cat.Works) == 0 && !allowEmpty {
		return emptyProjectionError(providers, present)
	}
	if len(publicSources) > 0 {
		allow := project.SourceSet(strings.Join(publicSources, ","))
		if stripped := project.SanitizeSources(cat, allow); stripped > 0 {
			fmt.Fprintf(os.Stderr, "project: stripped %d private source attributions from the public catalog\n", stripped)
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
	redirects, err := project.Redirects(b)
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "redirects.json"), redirects); err != nil {
		return err
	}
	fmt.Printf("projected %d works to %s (schema v%d); facets: %d languages, %d subjects, %d contributors; %d redirects\n",
		len(cat.Works), out, project.SchemaVersion,
		len(facets.Languages), len(facets.Subjects), len(facets.Contributors), len(redirects.Redirects))
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
