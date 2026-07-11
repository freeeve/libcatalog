package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/freeeve/libcat/bibframe"
)

// runMerge records an editorial merge decision by adding an
// lcat:mergedInto statement to the surviving Work's grain, so the decision is
// durable and preserved across re-ingest. The merge takes effect on the next
// ingest, which resolves the retired Work's Instances onto the survivor and drops
// the retired grain; the projector then emits a redirect for the retired id.
func runMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ExitOnError)
	dir := fs.String("dir", "", "grain directory (the ingest --out) holding the per-Work grains")
	from := fs.String("from", "", "retired Work id (its Instances move to --to)")
	to := fs.String("to", "", "surviving Work id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *from == "" || *to == "" {
		return fmt.Errorf("--dir, --from and --to are required")
	}
	if *from == *to {
		return fmt.Errorf("--from and --to must differ")
	}

	survivor := filepath.Join(*dir, filepath.FromSlash(bibframe.GrainPath(*to)))
	b, err := os.ReadFile(survivor)
	if err != nil {
		return fmt.Errorf("read surviving grain %s: %w", *to, err)
	}
	// The retired grain should exist; warn rather than fail if it does not, so a
	// merge can still be recorded when the retired grain was already removed.
	retired := filepath.Join(*dir, filepath.FromSlash(bibframe.GrainPath(*from)))
	if _, err := os.Stat(retired); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "lcat merge: warning: no grain for retired id %s at %s\n", *from, retired)
	}

	merged, err := bibframe.AddMergeMarker(b, *from, *to)
	if err != nil {
		return err
	}
	if err := os.WriteFile(survivor, merged, 0o644); err != nil {
		return err
	}
	fmt.Printf("recorded merge %s -> %s in %s; re-run the ingest then `lcat project` to apply\n", *from, *to, survivor)
	return nil
}
