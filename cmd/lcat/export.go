package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/freeeve/libcat/export"
	"github.com/freeeve/libcat/project"
)

// runExport derives the downloadable artifacts from an ingest output root
// (tasks/172): catalog.nq.gz, catalog.mrc.gz, catalog.xml.gz, and an
// integrity manifest for the downloads page. --public-sources applies the
// same provenance allowlist to the nq download that `lcat project` applies
// to catalog.json; the on-disk graph of record stays complete.
func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	in := fs.String("in", "data/out", "grain root (contains data/works and the catalog.nq derived from it)")
	out := fs.String("out", "public/downloads", "output directory for the gzipped artifacts")
	manifest := fs.String("manifest", "", "manifest path the downloads page reads (default <out>/downloads.json)")
	publicSources := fs.String("public-sources", "",
		"comma-separated extra.sources names allowed in the nq download; others are stripped (tasks/172). Empty (default) keeps everything.")
	orgCode := fs.String("org-code", "",
		"deployment MARC organization code: derives each record's 040 from graph facts in the MARC downloads (tasks/192). Empty disables.")
	coversOut := fs.String("covers-out", "",
		"directory uploaded cover images are copied to (the site's covers/ dir, tasks/215). Empty skips.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := export.Options{In: *in, Out: *out, OrgCode: *orgCode, CoversOut: *coversOut}
	if *publicSources != "" {
		opts.PublicSources = project.SourceSet(*publicSources)
	}
	m, err := export.Run(opts)
	if err != nil {
		return err
	}
	path := *manifest
	if path == "" {
		path = filepath.Join(*out, "downloads.json")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := writeJSON(path, m); err != nil {
		return err
	}
	for _, f := range m.Files {
		fmt.Printf("%-16s %10d bytes  %d records  sha256:%s…\n", f.Name, f.Bytes, f.Records, f.SHA256[:12])
	}
	fmt.Printf("exported %d works to %s; manifest %s\n", m.Works, *out, path)
	return nil
}
