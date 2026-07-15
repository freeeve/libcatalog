package main

import (
	"flag"
	"fmt"

	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/ingest/overdrive"
)

// runOverdrive ingests a cached OverDrive scan (page-*.json), mapping the Thunder JSON
// directly to canonical BIBFRAME grains with stable, minted two-tier ids
// (ARCHITECTURE §4/§9) via the OverDrive ingest provider and the shared ingest.Run
// pipeline: any grains already under --out seed the resolver, so re-ingest reuses ids
// and clusters editions into one Work. It is a convenience alias for
// `lcat ingest --provider overdrive`.
func runOverdrive(args []string) error {
	fs := flag.NewFlagSet("overdrive", flag.ExitOnError)
	cache := fs.String("cache", "", "OverDrive page-cache directory (contains page-*.json); omit to fetch live via --library")
	library := fs.String("library", "", "OverDrive library key to fetch live from the thunder API (when --cache is omitted)")
	writeCache := fs.String("write-cache", "", "with --library, also mirror fetched pages into this directory (reusable page cache)")
	perPage := fs.Int("per-page", 0, "live page size (default 200)")
	out := fs.String("out", "", "output directory for canonical grains (direct JSON->BIBFRAME)")
	provider := fs.String("provider", overdrive.ProviderName, "provenance graph feed:<provider> for the records")
	reconcile := fs.String("reconcile", "", "flag feed-only works this scan no longer lists: review | auto-suppress")
	allowEmpty := fs.Bool("reconcile-allow-empty", false, "let a zero-record scan reconcile (withdraws every feed-only work)")
	ownedOnly := fs.Bool("owned-only", false, "ingest only titles the library holds (isOwned or ownedCopies>0)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cache == "" && *library == "" {
		return fmt.Errorf("one of --cache (offline page cache) or --library (live fetch) is required")
	}
	if *cache != "" && *library != "" {
		return fmt.Errorf("--cache and --library are mutually exclusive: --cache reads offline, --library fetches live")
	}
	if *out == "" {
		return fmt.Errorf("--out (grains output directory) is required")
	}
	cfg := ingest.Config{Feed: *provider, Source: *cache, Params: map[string]string{}}
	if *ownedOnly {
		cfg.Params["ownedOnly"] = "true"
	}
	if *library != "" {
		cfg.Params["library"] = *library
		if *writeCache != "" {
			cfg.Params["writeCache"] = *writeCache
		}
		if *perPage > 0 {
			cfg.Params["perPage"] = fmt.Sprintf("%d", *perPage)
		}
	}
	return runIngest(providerRegistry(), overdrive.ProviderName, cfg, *out, *reconcile, *allowEmpty)
}
