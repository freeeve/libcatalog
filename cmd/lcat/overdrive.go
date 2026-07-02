package main

import (
	"flag"
	"fmt"

	"github.com/freeeve/libcatalog/ingest"
	"github.com/freeeve/libcatalog/ingest/overdrive"
)

// runOverdrive ingests a cached OverDrive scan (page-*.json), mapping the Thunder JSON
// directly to canonical BIBFRAME grains with stable, minted two-tier ids
// (ARCHITECTURE §4/§9) via the OverDrive ingest provider and the shared ingest.Run
// pipeline: any grains already under --out seed the resolver, so re-ingest reuses ids
// and clusters editions into one Work. It is a convenience alias for
// `lcat ingest --provider overdrive`.
func runOverdrive(args []string) error {
	fs := flag.NewFlagSet("overdrive", flag.ExitOnError)
	cache := fs.String("cache", "", "OverDrive page-cache directory (contains page-*.json)")
	out := fs.String("out", "", "output directory for canonical grains (direct JSON->BIBFRAME)")
	provider := fs.String("provider", overdrive.ProviderName, "provenance graph feed:<provider> for the records")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *cache == "" {
		return fmt.Errorf("--cache is required")
	}
	if *out == "" {
		return fmt.Errorf("--out (grains output directory) is required")
	}
	cfg := ingest.Config{Feed: *provider, Source: *cache}
	return runIngest(providerRegistry(), overdrive.ProviderName, cfg, *out)
}
