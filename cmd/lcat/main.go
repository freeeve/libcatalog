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
	"github.com/freeeve/libcatalog/storage"
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
	case "project":
		if err := runProject(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat project:", err)
			os.Exit(1)
		}
	case "serialize":
		if err := runSerialize(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat serialize:", err)
			os.Exit(1)
		}
	case "merge":
		if err := runMerge(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat merge:", err)
			os.Exit(1)
		}
	case "split":
		if err := runSplit(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat split:", err)
			os.Exit(1)
		}
	case "index":
		if err := runIndex(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat index:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
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
	fmt.Fprintln(os.Stderr, "  lcat project --catalog <catalog.nq> [--out <dir>] [--provider <name>]")
	fmt.Fprintln(os.Stderr, "  lcat serialize --dir <grains>   (regenerate catalog.nq from committed grains)")
	fmt.Fprintln(os.Stderr, "  lcat index --catalog <catalog.json> [--out <dir>]")
	fmt.Fprintln(os.Stderr, "  lcat merge --dir <grains> --from <workid> --to <workid>")
	fmt.Fprintln(os.Stderr, "  lcat split --dir <grains> --from <workid> --instances <instid,instid,...>")
}
