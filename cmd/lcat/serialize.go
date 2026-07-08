package main

import (
	"flag"
	"fmt"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage"
)

// runSerialize regenerates catalog.nq from the committed per-Work grains under
// --dir, without re-ingesting from a provider cache. Use it after hand-editing
// grains or an editorial overlay (lcat merge/split) to refresh the bulk file the
// projector consumes; it is provider-agnostic and does not fold in feed data, so
// unlike a re-ingest it needs no source cache.
func runSerialize(args []string) error {
	fs := flag.NewFlagSet("serialize", flag.ExitOnError)
	dir := fs.String("dir", "", "grain directory (holds data/works/*.nq); catalog.nq is (re)written here")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" {
		return fmt.Errorf("--dir is required")
	}

	n, err := bibframe.SerializeGrains(*dir, storage.Dir(*dir))
	if err != nil {
		return err
	}
	fmt.Printf("serialized %d grains to %s/catalog.nq\n", n, *dir)
	return nil
}
