package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/freeeve/libcat/diversity"
	"github.com/freeeve/libcat/project"
)

// runAudit reports the content-diversity profile of a projected catalog: how many
// works fall into each diversity category, derived from their controlled subjects
// via the diversity crosswalk (tasks/365/366). It reads catalog.json (the `lcat
// project` output, which carries each subject's authority URI and heading labels),
// never the graph, and it is coverage-first: every category share is stated against
// an explicit denominator so undercounting is visible.
//
// This measures what works are ABOUT, from their subject headings. It says nothing
// about the identity of their creators -- that is a separate, opt-in axis
// (tasks/368).
func runAudit(args []string) error {
	fs := flag.NewFlagSet("audit", flag.ExitOnError)
	catalogJSON := fs.String("catalog", "", "path to a projected catalog.json (from `lcat project`)")
	crosswalk := fs.String("crosswalk", "",
		"comma-separated operator override crosswalk TOML file(s), merged over the built-in seed (tasks/365)")
	format := fs.String("format", "text", "output format: text or json")
	out := fs.String("out", "", "write the report to this file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *catalogJSON == "" {
		return fmt.Errorf("--catalog is required (the catalog.json from `lcat project`)")
	}
	if *format != "text" && *format != "json" {
		return fmt.Errorf("--format must be text or json, got %q", *format)
	}

	data, err := os.ReadFile(*catalogJSON)
	if err != nil {
		return fmt.Errorf("read catalog: %w", err)
	}
	var cat project.Catalog
	if err := json.Unmarshal(data, &cat); err != nil {
		return fmt.Errorf("parse catalog.json: %w", err)
	}

	cw, err := diversity.Load(splitList(*crosswalk)...)
	if err != nil {
		return err
	}

	a := diversity.NewAuditor(cw)
	for _, w := range cat.Works {
		a.Add(subjectRefs(w))
	}
	report := a.Report()

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
		return enc.Encode(report)
	}
	return writeAuditText(w, report)
}

// subjectRefs turns a projected Work's subjects into audit SubjectRefs: the
// authority URI plus every localized heading label, so both controlled (URI) and
// bare-string (label) subjects feed the crosswalk.
func subjectRefs(w project.Work) []diversity.SubjectRef {
	refs := make([]diversity.SubjectRef, 0, len(w.Subjects))
	for _, s := range w.Subjects {
		labels := make([]string, 0, len(s.Labels))
		for _, l := range s.Labels {
			labels = append(labels, l)
		}
		refs = append(refs, diversity.SubjectRef{URI: s.ID, Labels: labels})
	}
	return refs
}

// writeAuditText renders the report as a human-readable, coverage-first summary.
func writeAuditText(f *os.File, r diversity.Report) error {
	fmt.Fprintln(f, "Content diversity audit")
	fmt.Fprintln(f, "=======================")
	fmt.Fprintf(f, "Works audited:   %d\n", r.TotalWorks)
	fmt.Fprintf(f, "With subjects:   %d (%.1f%% coverage)\n", r.CoveredWorks, r.Coverage*100)
	fmt.Fprintln(f)
	if r.CoveredWorks == 0 {
		fmt.Fprintln(f, "No works carry subjects, so no content audit is possible.")
		return nil
	}
	fmt.Fprintln(f, "Category shares are of the works that carry subjects (% subj.);")
	fmt.Fprintln(f, "% coll. is of the whole collection, including unsubjected works.")
	fmt.Fprintln(f)
	tw := tabwriter.NewWriter(f, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "Category\tWorks\t% subj.\t% coll.")
	fmt.Fprintln(tw, "--------\t-----\t-------\t-------")
	for _, c := range r.Categories {
		fmt.Fprintf(tw, "%s\t%d\t%.1f%%\t%.1f%%\n", c.Label, c.Works, c.ShareCovered*100, c.ShareTotal*100)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(f)
	fmt.Fprintln(f, "This measures what works are about (their subject headings), not")
	fmt.Fprintln(f, "the identity of their creators.")
	return nil
}
