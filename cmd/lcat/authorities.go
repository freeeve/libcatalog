package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/freeeve/libcat/catalogindex"
)

// runAuthorities reports how heavily each controlled subject authority is used
// across the whole corpus: for every authority any work is subject-linked to,
// the count of distinct works that reference it. It reads a catalog.nq dataset
// (the `lcat serialize` output) into an in-memory analytics graph and answers a
// cross-work question the per-work and workindex layers cannot -- the kind of
// question authority maintenance needs (which headings carry the collection,
// which are used by a single work and worth reviewing, coverage per scheme).
func runAuthorities(args []string) error {
	fs := flag.NewFlagSet("authorities", flag.ExitOnError)
	graph := fs.String("graph", "", "path to a catalog.nq dataset (from `lcat serialize`); also accepted as the first argument")
	scheme := fs.String("scheme", "", "report only authorities in this scheme (e.g. lcsh, fast, homosaurus)")
	minWorks := fs.Int("min-works", 0, "report only authorities used by at least this many works")
	maxWorks := fs.Int("max-works", 0, "report only authorities used by at most this many works (0 = no upper bound); --max-works 1 finds single-use headings")
	top := fs.Int("top", 0, "report only the N most-used authorities after filtering (0 = all)")
	format := fs.String("format", "text", "output format: text or json")
	out := fs.String("out", "", "write the report to this file instead of stdout")
	// A leading positional path is pulled out before flag parsing, since Go's
	// flag package stops at the first non-flag argument; this lets the natural
	// `authorities <catalog.nq> --max-works 1` form work as well as flags-first.
	var lead string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		lead, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	path := lead
	if path == "" {
		path = *graph
	}
	if path == "" {
		path = fs.Arg(0)
	}
	if path == "" {
		return fmt.Errorf("a catalog.nq path is required (as --graph or the first argument)")
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("--format must be text or json, got %q", *format)
	}

	snap, err := catalogindex.Open(path)
	if err != nil {
		return err
	}
	usage := snap.AuthorityUsage()
	total := len(usage)
	usage = filterAuthorities(usage, *scheme, *minWorks, *maxWorks)
	shown := len(usage)
	if *top > 0 && *top < len(usage) {
		usage = usage[:*top]
	}

	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return fmt.Errorf("create --out: %w", err)
		}
		defer f.Close()
		w = f
	}
	if *format == "json" {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(usage)
	}
	return writeAuthoritiesText(w, usage, total, shown, *scheme)
}

// filterAuthorities keeps only the authorities matching the scheme and the
// inclusive work-count window; a maxWorks of 0 means no upper bound.
func filterAuthorities(usage []catalogindex.AuthorityUse, scheme string, minWorks, maxWorks int) []catalogindex.AuthorityUse {
	kept := usage[:0:0]
	for _, u := range usage {
		if scheme != "" && u.Scheme != scheme {
			continue
		}
		if u.Works < minWorks {
			continue
		}
		if maxWorks > 0 && u.Works > maxWorks {
			continue
		}
		kept = append(kept, u)
	}
	return kept
}

// writeAuthoritiesText renders the usage report as a human-readable table,
// stating what fraction of the corpus's authorities it covers so a scoped
// report says what it left out.
func writeAuthoritiesText(f *os.File, usage []catalogindex.AuthorityUse, total, shown int, scheme string) error {
	fmt.Fprintln(f, "Controlled subject authority usage")
	fmt.Fprintln(f, "==================================")
	fmt.Fprintf(f, "Authorities in corpus: %d\n", total)
	if scheme != "" {
		fmt.Fprintf(f, "Scheme:                %s\n", scheme)
	}
	if shown != total {
		fmt.Fprintf(f, "Matching filter:       %d\n", shown)
	}
	fmt.Fprintf(f, "Listed:                %d\n", len(usage))
	fmt.Fprintln(f)
	if len(usage) == 0 {
		fmt.Fprintln(f, "No authorities match.")
		return nil
	}
	tw := tabwriter.NewWriter(f, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "Works\tScheme\tLabel\tURI")
	fmt.Fprintln(tw, "-----\t------\t-----\t---")
	for _, u := range usage {
		label := u.Label
		if label == "" {
			label = "(no label)"
		}
		scheme := u.Scheme
		if scheme == "" {
			scheme = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", u.Works, scheme, label, u.URI)
	}
	return tw.Flush()
}
