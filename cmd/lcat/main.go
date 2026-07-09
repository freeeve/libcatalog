// Command lcat is libcat's projector CLI: it ingests bibliographic sources
// into canonical BIBFRAME grains (and, later, projects them for the Hugo
// module). It is storage- and compute-agnostic: this same binary is the
// entrypoint for a local run or a Fargate/container task, and a cloud-function
// handler wraps the same core over a cloud Sink.
package main

import (
	"fmt"
	"os"
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
	case "ingest":
		if err := runIngestCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat ingest:", err)
			os.Exit(1)
		}
	case "overdrive":
		if err := runOverdrive(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat overdrive:", err)
			os.Exit(1)
		}
	case "hardcover":
		if err := runHardcover(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat hardcover:", err)
			os.Exit(1)
		}
	case "project":
		if err := runProject(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat project:", err)
			os.Exit(1)
		}
	case "export":
		if err := runExport(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat export:", err)
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
	case "rebuild":
		if err := runRebuild(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat rebuild:", err)
			os.Exit(1)
		}
	case "vocab-subset":
		if err := runVocabSubset(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat vocab-subset:", err)
			os.Exit(1)
		}
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat serve:", err)
			os.Exit(1)
		}
	case "covers":
		if err := runCovers(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "lcat covers:", err)
			os.Exit(1)
		}
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  lcat ingest --provider <name> --source <input> --out <dir> [--feed <name>] [--mapping <toml>] [--param k=v]...")
	fmt.Fprintln(os.Stderr, "      providers: overdrive (--source <page-cache dir>), marc (--source <file.mrc>),")
	fmt.Fprintln(os.Stderr, "                 nquads (--source <file.nq> --mapping <toml>), csv (--source <file.csv> --mapping <toml>)")
	fmt.Fprintln(os.Stderr, "  lcat overdrive --cache <dir> --out <dir> [--marc <file.mrc>] [--provider <name>]")
	fmt.Fprintln(os.Stderr, "  lcat hardcover --out <dir> [--token <tok>|$HARDCOVER_API_TOKEN] [--limit <n>] [--source <shelf.json>] [--introspect <type>]")
	fmt.Fprintln(os.Stderr, "  lcat build [--config lcat.toml] [--only step,step]")
	fmt.Fprintln(os.Stderr, "      whole pipeline from one deployment config: ingest -> serialize -> project -> export -> index -> hugo")
	fmt.Fprintln(os.Stderr, "  lcat project --catalog <catalog.nq> [--out <dir>] [--provider <a,b,...>] [--public-sources <a,b,...>]")
	fmt.Fprintln(os.Stderr, "              [--allow-empty]   (else projecting 0 works fails rather than publishing an empty catalog)")
	fmt.Fprintln(os.Stderr, "  lcat export [--in <dir>] [--out <dir>] [--manifest <file>] [--public-sources <a,b,...>]")
	fmt.Fprintln(os.Stderr, "              (downloads: catalog.nq.gz + catalog.mrc.gz + catalog.xml.gz + integrity manifest)")
	fmt.Fprintln(os.Stderr, "  lcat serialize --dir <grains>   (regenerate catalog.nq from committed grains)")
	fmt.Fprintln(os.Stderr, "  lcat index --catalog <catalog.json> [--out <dir>]")
	fmt.Fprintln(os.Stderr, "  lcat serve [--dir public] [--addr 127.0.0.1:8500]   (Range-capable preview of a built site)")
	fmt.Fprintln(os.Stderr, "  lcat rebuild --store <blob-root> --out <dir> [--index-out <dir>] [--cursor <file>] [--full]")
	fmt.Fprintln(os.Stderr, "               (feed-driven incremental: re-projects only grains changed since the cursor)")
	fmt.Fprintln(os.Stderr, "  lcat vocab-subset --catalog <catalog.json> --out <lcsh.nq> [--scheme lcsh] [--namespace <uri-prefix>]")
	fmt.Fprintln(os.Stderr, "                    [--fetch-suffix .nt] [--dump <url-or-file> [--all]]   (non-LoC authorities, e.g. Homosaurus)")
	fmt.Fprintln(os.Stderr, "                    [--from-catalog]   (no network: snapshot from catalog.json's own labels, e.g. FAST)")
	fmt.Fprintln(os.Stderr, "  lcat covers --store <blob-root> [--reap] [--json]")
	fmt.Fprintln(os.Stderr, "              (cover blobs no grain references; they keep serving publicly until reaped)")
	fmt.Fprintln(os.Stderr, "  lcat merge --dir <grains> --from <workid> --to <workid>")
	fmt.Fprintln(os.Stderr, "  lcat split --dir <grains> --from <workid> --instances <instid,instid,...>")
}
