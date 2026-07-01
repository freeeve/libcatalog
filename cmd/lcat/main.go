// Command lcat is libcatalog's projector CLI: it ingests bibliographic sources
// into canonical BIBFRAME grains (and, later, projects them for the Hugo
// module). It is storage- and compute-agnostic: this same binary is the
// entrypoint for a local run or a Fargate/container task, and a cloud-function
// handler wraps the same core over a cloud Sink.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/ingest/overdrive"
	"github.com/freeeve/libcatalog/storage"
	"github.com/freeeve/libcodex/iso2709"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "build":
		if err := runBuild(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat build:", err)
			os.Exit(1)
		}
	case "overdrive":
		if err := runOverdrive(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat overdrive:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

// runOverdrive ingests a cached OverDrive scan (page-*.json). With --out it maps
// the Thunder JSON directly to canonical BIBFRAME grains (the OverDrive reference
// provider, ARCHITECTURE §9). With --marc it also exports an ISO 2709 file -- a
// MARC Express stand-in for exercising the separate MARC-import ramp (tasks/007).
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
		entries := make([]bibframe.Entry, 0, len(items))
		for _, it := range items {
			id := it.WorkID()
			entries = append(entries, bibframe.Entry{ID: id, Base: id, Bib: it.BIBFRAME()})
		}
		stats, err := bibframe.BuildEntries(storage.Dir(*out), entries, *provider)
		if err != nil {
			return err
		}
		fmt.Printf("built %d grains directly from %d OverDrive records under %s (feed:%s)\n",
			stats.Grains, stats.Records, *out, *provider)
	}
	return nil
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

// runBuild ingests a MARC file into canonical grains + catalog.nq under --out.
func runBuild(args []string) error {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	marc := fs.String("marc", "", "path to an ISO 2709 MARC file (e.g. an OverDrive MARC Express export)")
	out := fs.String("out", ".", "output directory for grains and catalog.nq")
	provider := fs.String("provider", "overdrive", "provenance graph feed:<provider> for the ingested records")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *marc == "" {
		return fmt.Errorf("--marc is required")
	}

	f, err := os.Open(*marc)
	if err != nil {
		return err
	}
	defer f.Close()

	stats, err := bibframe.BuildMARC(storage.Dir(*out), f, *provider)
	if err != nil {
		return err
	}
	fmt.Printf("built %d grains from %d records under %s (feed:%s)\n",
		stats.Grains, stats.Records, *out, *provider)
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  lcat overdrive --cache <dir> --out <dir> [--marc <file.mrc>] [--provider <name>]")
	fmt.Fprintln(os.Stderr, "  lcat build --marc <file.mrc> [--out <dir>] [--provider <name>]")
}
