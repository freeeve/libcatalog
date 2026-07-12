package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/freeeve/libcat/storage/blob"
	"github.com/freeeve/libcat/storage/vocabsidecar"
)

// defaultAuthoritiesPrefix roots the authority tree in the blob store; it matches
// vocabsrc's default (LCATD_AUTHORITIES_PREFIX overrides it there, --prefix here).
const defaultAuthoritiesPrefix = "data/authorities/"

// runVocabGC reports, and with --reap deletes, the sidecar artifact sets no live
// snapshot backs.
//
// RemoveSnapshot deletes a scheme's sidecar nowadays, but a removal before
// that shipped left the artifacts resident, and nothing collects them: the loader
// detects the same staleness at boot and serves the scheme from maps, but leaves the
// files where they are. They cost object storage and, worse, make the sidecar
// directory lie about what is installed -- the first place anyone debugging vocabulary
// loading looks. This is the offline broom.
func runVocabGC(args []string) error {
	fs := flag.NewFlagSet("vocab-gc", flag.ExitOnError)
	store := fs.String("store", "", "blob root (holds data/authorities)")
	prefix := fs.String("prefix", defaultAuthoritiesPrefix, "authorities prefix within the store")
	reap := fs.Bool("reap", false, "delete the orphaned sidecars (default: report only)")
	asJSON := fs.Bool("json", false, "emit the orphan list as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *store == "" {
		return errors.New("--store is required")
	}
	ctx := context.Background()
	bs := blob.NewDir(*store)

	orphans, err := vocabsidecar.OrphanSidecars(ctx, bs, *prefix)
	if err != nil {
		return err
	}
	reaped := 0
	if *reap {
		for _, o := range orphans {
			if err := vocabsidecar.RemoveSidecar(ctx, bs, *prefix, o.Scheme); err != nil {
				return err
			}
			reaped++
		}
	}
	if *asJSON {
		if orphans == nil {
			orphans = []vocabsidecar.OrphanSidecar{} // a clean run is [], not null, for jq
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"orphans": orphans, "reaped": reaped})
	}
	return printVocabGCReport(orphans, *reap)
}

func printVocabGCReport(orphans []vocabsidecar.OrphanSidecar, reaped bool) error {
	if len(orphans) == 0 {
		fmt.Println("no orphan sidecars: every manifest names a snapshot that exists")
		return nil
	}
	for _, o := range orphans {
		line := fmt.Sprintf("  %s  (%s", o.Scheme, o.Reason)
		if o.Source != "" {
			line += ", named " + o.Source
		}
		fmt.Println(line + ")")
	}
	verb := "orphaned"
	if reaped {
		verb = "deleted"
	}
	fmt.Printf("%d %s sidecar%s\n", len(orphans), verb, plural(len(orphans)))
	if !reaped {
		fmt.Println("re-run with --reap to delete them")
	}
	return nil
}
