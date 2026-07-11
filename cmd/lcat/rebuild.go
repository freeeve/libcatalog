package main

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/project"
	"github.com/freeeve/libcat/search"
	"github.com/freeeve/libcat/storage"
)

// The feed-driven incremental public rebuild: instead of the full
// serialize -> project -> index chain over the whole corpus, consume the
// work-index change feed the backend maintains (backend), re-project
// only the grains that changed since the stored cursor, patch catalog.json /
// facets.json / redirects.json in place, and re-emit the search artifacts from
// the patched catalog. The search/browse emit is the interim monolith rebuild
// -- in-memory over the already-loaded catalog, seconds at current scale; the
// splitset base+delta switch is deferred until corpus size demands it.
//
// Any doubt falls back to the full chain: no feed, no cursor, a fold (feed
// epoch advanced past the cursor), or an artifact schema bump. Scheduled full
// rebuilds remain the correctness backstop; an incremental run leaves
// catalog.nq untouched (nothing downstream reads it once catalog.json is
// patched directly).

// rebuildCursorVersion is the cursor file schema version.
const rebuildCursorVersion = 1

// rebuildCursor persists where the incremental rebuild left off: the feed's
// fold epoch, how many of that epoch's records the outputs already reflect,
// and the artifact schema versions so a schema bump forces a full rebuild.
type rebuildCursor struct {
	Version       int    `json:"version"`
	Epoch         uint64 `json:"epoch"`
	Applied       int    `json:"applied"`
	ProjectSchema int    `json:"projectSchema"`
	SearchSchema  int    `json:"searchSchema"`
}

// rebuildFeed mirrors the JSON shape of the backend work index's change feed
// (backend/workindex/feed.go, data/workindex.feed). lcat lives in the root
// module and cannot import the backend module, so it decodes just the fields
// it needs; unknown fields are ignored.
type rebuildFeed struct {
	Version int    `json:"version"`
	Epoch   uint64 `json:"epoch"`
	Records []struct {
		Path    string `json:"path"`
		Deleted bool   `json:"deleted"`
	} `json:"records"`
}

func runRebuild(args []string) error {
	fs := flag.NewFlagSet("rebuild", flag.ExitOnError)
	store := fs.String("store", "", "blob store root (holds data/works/ and data/workindex.feed)")
	out := fs.String("out", "", "projection output dir (catalog.json, facets.json, redirects.json)")
	indexOut := fs.String("index-out", "", "search artifact output dir (default: --out)")
	cursorPath := fs.String("cursor", "", "cursor file (default: <out>/rebuild-cursor.json)")
	provider := fs.String("provider", "overdrive", "provenance graph feed:<provider> to project")
	full := fs.Bool("full", false, "force the full serialize+project+index chain")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *store == "" || *out == "" {
		return fmt.Errorf("--store and --out are required")
	}
	if *indexOut == "" {
		*indexOut = *out
	}
	if *cursorPath == "" {
		*cursorPath = filepath.Join(*out, "rebuild-cursor.json")
	}

	// Read the feed head first; the cursor written on success reflects this
	// state, so records appended mid-run are simply picked up next run.
	feed, feedErr := readRebuildFeed(filepath.Join(*store, "data", "workindex.feed"))
	if errors.Is(feedErr, os.ErrNotExist) {
		// No feed just means nothing was written since the last fold -- for a
		// server-managed store (it has a snapshot), the snapshot's epoch is
		// the baseline. A store with neither is not feed-managed (a pure
		// ingest corpus); the full chain runs every time.
		if epoch, ok := readSnapshotEpoch(*store); ok {
			feed, feedErr = rebuildFeed{Version: 1, Epoch: epoch}, nil
		}
	}
	head := rebuildCursor{
		Version:       rebuildCursorVersion,
		ProjectSchema: project.SchemaVersion,
		SearchSchema:  search.SchemaVersion,
	}
	if feedErr == nil {
		head.Epoch, head.Applied = feed.Epoch, len(feed.Records)
	}

	reason := ""
	var changed []string
	if *full {
		reason = "--full"
	} else if changed, reason = incrementalDelta(feedErr, feed, *cursorPath); reason == "" {
		if len(changed) == 0 {
			fmt.Println("up to date: no feed records since the cursor")
			return writeJSON(*cursorPath, head)
		}
		n, err := rebuildIncremental(*store, *out, *indexOut, *provider, changed)
		if err == nil {
			fmt.Printf("incremental rebuild: %d changed grains re-projected (catalog.nq untouched; full rebuilds refresh it)\n", n)
			return writeJSON(*cursorPath, head)
		}
		var nf needFullError
		if !errors.As(err, &nf) {
			return err
		}
		reason = nf.reason
	}

	fmt.Printf("full rebuild (%s)\n", reason)
	if err := rebuildFull(*store, *out, *indexOut, *provider); err != nil {
		return err
	}
	return writeJSON(*cursorPath, head)
}

// needFullError signals that the incremental path found its inputs unusable
// (missing or schema-stale artifacts) and the caller should run the full chain.
type needFullError struct{ reason string }

func (e needFullError) Error() string { return "rebuild: full rebuild needed: " + e.reason }

// incrementalDelta decides whether the feed + cursor admit an incremental run:
// it returns the changed grain paths since the cursor, or the reason they
// cannot be known (no feed, no cursor, a fold, a schema bump).
func incrementalDelta(feedErr error, feed rebuildFeed, cursorPath string) ([]string, string) {
	if feedErr != nil {
		return nil, fmt.Sprintf("feed unreadable: %v", feedErr)
	}
	cur, err := readRebuildCursor(cursorPath)
	if err != nil {
		return nil, fmt.Sprintf("cursor unreadable: %v", err)
	}
	if cur.Version != rebuildCursorVersion {
		return nil, "cursor version changed"
	}
	if cur.ProjectSchema != project.SchemaVersion || cur.SearchSchema != search.SchemaVersion {
		return nil, "artifact schema changed"
	}
	if cur.Epoch != feed.Epoch || cur.Applied > len(feed.Records) {
		return nil, fmt.Sprintf("feed folded (cursor epoch %d/%d, feed epoch %d/%d)",
			cur.Epoch, cur.Applied, feed.Epoch, len(feed.Records))
	}
	seen := map[string]bool{}
	var paths []string
	for _, r := range feed.Records[cur.Applied:] {
		if r.Path != "" && !seen[r.Path] {
			seen[r.Path] = true
			paths = append(paths, r.Path)
		}
	}
	sort.Strings(paths)
	return paths, ""
}

// rebuildIncremental patches the projection artifacts for the changed grains
// and re-emits the search artifacts from the patched catalog. Whether a grain
// was deleted is decided by looking at the store, not the feed record, so
// out-of-order records self-heal. Returns the changed-grain count.
//
// Per-grain projection matches the full-catalog projection because grains are
// self-contained: minted Works are "#<id>Work" fragment IRIs and each grain
// carries its own subject labels and merge markers.
func rebuildIncremental(store, out, indexOut, provider string, changed []string) (int, error) {
	catBytes, err := os.ReadFile(filepath.Join(out, "catalog.json"))
	if err != nil {
		return 0, needFullError{reason: fmt.Sprintf("catalog.json unreadable: %v", err)}
	}
	var cat project.Catalog
	if err := json.Unmarshal(catBytes, &cat); err != nil {
		return 0, needFullError{reason: fmt.Sprintf("catalog.json unparsable: %v", err)}
	}
	if cat.Version != project.SchemaVersion {
		return 0, needFullError{reason: fmt.Sprintf("catalog.json schema v%d, want v%d", cat.Version, project.SchemaVersion)}
	}
	redirects := project.RedirectMap{Version: project.SchemaVersion, Redirects: []project.Redirect{}}
	if b, err := os.ReadFile(filepath.Join(out, "redirects.json")); err == nil {
		if err := json.Unmarshal(b, &redirects); err != nil {
			return 0, needFullError{reason: fmt.Sprintf("redirects.json unparsable: %v", err)}
		}
	}

	// Drop every entry the changed grains own, then re-project the grains that
	// still exist. A work's grain path is derivable from its id (GrainPath),
	// and a redirect is recorded on the retired work's own grain.
	isChanged := map[string]bool{}
	for _, p := range changed {
		isChanged[p] = true
	}
	works := cat.Works[:0]
	for _, w := range cat.Works {
		if !isChanged[bibframe.GrainPath(w.ID)] {
			works = append(works, w)
		}
	}
	cat.Works = works
	reds := redirects.Redirects[:0]
	for _, r := range redirects.Redirects {
		if !isChanged[bibframe.GrainPath(r.From)] {
			reds = append(reds, r)
		}
	}
	redirects.Redirects = reds

	for _, p := range changed {
		gb, err := os.ReadFile(filepath.Join(store, filepath.FromSlash(p)))
		if os.IsNotExist(err) {
			continue // deleted: its entries are already dropped
		}
		if err != nil {
			return 0, err
		}
		gcat, err := project.Project(gb, provider)
		if err != nil {
			return 0, fmt.Errorf("rebuild: project %s: %w", p, err)
		}
		cat.Works = append(cat.Works, gcat.Works...)
		rm, err := project.Redirects(gb)
		if err != nil {
			return 0, fmt.Errorf("rebuild: redirects %s: %w", p, err)
		}
		redirects.Redirects = append(redirects.Redirects, rm.Redirects...)
	}
	sort.Slice(cat.Works, func(i, j int) bool { return cat.Works[i].ID < cat.Works[j].ID })
	sort.Slice(redirects.Redirects, func(i, j int) bool { return redirects.Redirects[i].From < redirects.Redirects[j].From })

	if err := writeProjection(out, &cat, redirects); err != nil {
		return 0, err
	}
	return len(changed), writeSearchArtifacts(indexOut, &cat)
}

// rebuildFull runs the classic whole-corpus chain: serialize grains into
// catalog.nq, project it, and build the search artifacts.
func rebuildFull(store, out, indexOut, provider string) error {
	if _, err := bibframe.SerializeGrains(store, storage.Dir(store)); err != nil {
		return err
	}
	b, err := os.ReadFile(filepath.Join(store, "catalog.nq"))
	if err != nil {
		return err
	}
	cat, err := project.Project(b, provider)
	if err != nil {
		return err
	}
	redirects, err := project.Redirects(b)
	if err != nil {
		return err
	}
	if err := writeProjection(out, cat, redirects); err != nil {
		return err
	}
	return writeSearchArtifacts(indexOut, cat)
}

// writeProjection writes the three projection artifacts, facets recomputed
// from the catalog.
func writeProjection(out string, cat *project.Catalog, redirects project.RedirectMap) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "catalog.json"), cat); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "facets.json"), cat.Facets()); err != nil {
		return err
	}
	return writeJSON(filepath.Join(out, "redirects.json"), redirects)
}

// writeSearchArtifacts emits the language indexes and the browse set from the
// in-memory catalog.
func writeSearchArtifacts(indexOut string, cat *project.Catalog) error {
	if err := os.MkdirAll(indexOut, 0o755); err != nil {
		return err
	}
	sink := storage.Dir(indexOut)
	if _, err := search.BuildIndexes(cat, sink); err != nil {
		return err
	}
	return search.BuildBrowse(cat, sink)
}

func readRebuildFeed(path string) (rebuildFeed, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return rebuildFeed{}, err
	}
	var f rebuildFeed
	if err := json.Unmarshal(b, &f); err != nil {
		return rebuildFeed{}, err
	}
	return f, nil
}

// readSnapshotEpoch reads the fold epoch from the work-index snapshot
// (backend gzipped JSON) -- the cursor baseline when the feed does
// not exist yet.
func readSnapshotEpoch(store string) (uint64, bool) {
	f, err := os.Open(filepath.Join(store, "data", "workindex.snapshot"))
	if err != nil {
		return 0, false
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, false
	}
	defer gz.Close()
	var s struct {
		Epoch uint64 `json:"epoch"`
	}
	if json.NewDecoder(gz).Decode(&s) != nil {
		return 0, false
	}
	return s.Epoch, true
}

func readRebuildCursor(path string) (rebuildCursor, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return rebuildCursor{}, err
	}
	var c rebuildCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return rebuildCursor{}, err
	}
	return c, nil
}
