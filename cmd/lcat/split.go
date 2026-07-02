package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/identity"
)

// runSplit records an editorial split decision (tasks/001): it mints a new Work id
// and pins the given Instances to it via lcat:workAssignment statements (plus an
// lcat:splitFrom provenance link) in the source Work's grain. The pins survive
// re-ingest and override the computed clustering key, so an over-merge the key
// would otherwise recreate stays split. The split takes effect on the next ingest,
// which regroups the pinned Instances onto the new Work.
func runSplit(args []string) error {
	fs := flag.NewFlagSet("split", flag.ExitOnError)
	dir := fs.String("dir", "", "grain directory (the ingest --out) holding the per-Work grains")
	from := fs.String("from", "", "over-merged Work id to split Instances out of")
	instances := fs.String("instances", "", "comma-separated Instance ids to move to the new Work")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || *from == "" || *instances == "" {
		return fmt.Errorf("--dir, --from and --instances are required")
	}
	ids := splitList(*instances)
	if len(ids) == 0 {
		return fmt.Errorf("--instances lists no Instance ids")
	}

	source := filepath.Join(*dir, filepath.FromSlash(bibframe.GrainPath(*from)))
	b, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read source grain %s: %w", *from, err)
	}

	newWork := identity.Mint(identity.WorkPrefix)
	marked, err := bibframe.AddSplitMarkers(b, newWork, *from, ids)
	if err != nil {
		return err
	}
	if err := os.WriteFile(source, marked, 0o644); err != nil {
		return err
	}
	fmt.Printf("recorded split of %d instances from %s into new work %s in %s; re-run the ingest then `lcat project` to apply\n",
		len(ids), *from, newWork, source)
	return nil
}

// splitList parses a comma-separated id list, dropping blanks and surrounding
// whitespace.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
