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
// integrity manifest for the downloads page.
//
// These artifacts are the public site's, so they describe the public collection:
// a suppressed or tombstoned Work is absent from all of them and its cover is not
// copied, exactly as `lcat project` omits it from catalog.json (tasks/304). The
// complete graph of record -- every Work, hidden or not -- lives in the grain tree
// and is reachable through the librarian-gated backend export service. It is never
// written into a directory the site serves.
//
// --public-sources and --public-extras apply the same allowlists to the nq
// download that `lcat project` applies to catalog.json. All three filters answer
// the same question: what may the public see. None of them touches the grains.
// With --public-extras set but not naming `cover`, the covers are not copied
// either: the public catalog cannot name them, so no public page renders them.
func runExport(args []string) error {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	in := fs.String("in", "data/out", "grain root (contains data/works and the catalog.nq derived from it)")
	out := fs.String("out", "public/downloads", "output directory for the gzipped artifacts")
	manifest := fs.String("manifest", "", "manifest path the downloads page reads (default <out>/downloads.json)")
	publicSources := fs.String("public-sources", "",
		"comma-separated extra.sources names allowed in the nq download; others are stripped. Empty (default) keeps everything.")
	publicExtras := fs.String("public-extras", "",
		"comma-separated extra keys allowed in the nq download; other lcat:extra quads are dropped. `sources` is governed by --public-sources instead. Empty (default) keeps everything.")
	orgCode := fs.String("org-code", "",
		"deployment MARC organization code: derives each record's 040 from graph facts in the MARC downloads. Empty disables.")
	coversOut := fs.String("covers-out", "",
		"directory uploaded cover images are copied to (the site's covers/ dir). Empty skips.")
	if err := fs.Parse(args); err != nil {
		return err
	}
	opts := export.Options{In: *in, Out: *out, OrgCode: *orgCode, CoversOut: *coversOut}
	if *publicSources != "" {
		opts.PublicSources = project.SourceSet(*publicSources)
	}
	if *publicExtras != "" {
		opts.PublicExtras = project.SourceSet(*publicExtras)
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
