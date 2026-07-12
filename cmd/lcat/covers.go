package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// coverPrefix is the blob tree bibframe.CoverBlobPath writes under.
const coverPrefix = "data/covers/"

var coverWorkID = regexp.MustCompile(`^w[a-z0-9]{6,20}$`)

// Orphan reasons, distinguished because they mean different things to an
// operator: a stale format is a replaced cover's leftover bytes, a missing work is a
// hand-deleted grain, and an unparseable path is something that never came from
// CoverBlobPath at all.
//
// Not a tombstone: tombstoning and suppression are editorial statements that
// leave the grain in place and its cover claim standing, so a hidden Work is an
// orphan by none of these reasons and --reap never touches it. Keeping a hidden
// Work's cover in the *store* is correct -- both stances are reversible. Keeping
// it out of the *public site* is the exporter's job, and it does that now
// ; reaping a blob after it has reached a CDN does not unpublish it.
const (
	reasonStaleFormat = "the work's cover is a different format"
	reasonNoCover     = "the work has no cover"
	reasonNoWork      = "no grain for this work"
	reasonBadPath     = "not a cover path"
)

// orphanCover is one blob no grain references.
type orphanCover struct {
	Path   string `json:"path"`
	WorkID string `json:"workId,omitempty"`
	Reason string `json:"reason"`
	// Referenced is the cover the work does claim, when it claims one. An
	// external URL here means every local blob for the work is an orphan.
	Referenced string `json:"referenced,omitempty"`
	Size       int64  `json:"size"`
}

// runCovers reports, and with --reap deletes, the cover blobs no grain
// references.
//
// stopped cover replacement from leaving the previous format behind.
// It did not clean up what earlier replacements had already left: those images
// are still served from a public, unauthenticated, guessable URL, and nothing
// in the catalog points at them, so nothing would ever collect them. A takedown
// that looks done is not done.
func runCovers(args []string) error {
	fs := flag.NewFlagSet("covers", flag.ExitOnError)
	store := fs.String("store", "", "blob root (holds data/works and data/covers)")
	reap := fs.Bool("reap", false, "delete the orphaned blobs (default: report only)")
	asJSON := fs.Bool("json", false, "emit the orphan list as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *store == "" {
		return errors.New("--store is required")
	}
	ctx := context.Background()
	bs := blob.NewDir(*store)

	orphans, scanned, err := findOrphanCovers(ctx, bs)
	if err != nil {
		return err
	}
	var reaped []orphanCover
	if *reap {
		for _, o := range orphans {
			if err := bs.Delete(ctx, o.Path); err != nil && !errors.Is(err, blob.ErrNotFound) {
				return fmt.Errorf("delete %s: %w", o.Path, err)
			}
			reaped = append(reaped, o)
		}
	}
	if *asJSON {
		out := map[string]any{"scanned": scanned, "orphans": orphans, "reaped": len(reaped)}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}
	return printCoverReport(scanned, orphans, *reap)
}

func printCoverReport(scanned int, orphans []orphanCover, reaped bool) error {
	verb := "orphaned"
	if reaped {
		verb = "deleted"
	}
	fmt.Printf("scanned %d cover blob%s\n", scanned, plural(scanned))
	if len(orphans) == 0 {
		fmt.Println("no orphans: every stored cover is the one its work references")
		return nil
	}
	byReason := map[string]int{}
	for _, o := range orphans {
		byReason[o.Reason]++
		line := fmt.Sprintf("  %s  (%s", o.Path, o.Reason)
		if o.Referenced != "" {
			line += ", references " + o.Referenced
		}
		fmt.Println(line + ")")
	}
	reasons := make([]string, 0, len(byReason))
	for r := range byReason {
		reasons = append(reasons, r)
	}
	sort.Strings(reasons)
	fmt.Printf("%d %s cover blob%s\n", len(orphans), verb, plural(len(orphans)))
	for _, r := range reasons {
		fmt.Printf("  %3d  %s\n", byReason[r], r)
	}
	if !reaped {
		fmt.Println("re-run with --reap to delete them")
	}
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// findOrphanCovers walks the cover tree and returns the blobs no grain claims,
// plus the number of blobs it looked at.
//
// Grains are read once per work rather than once per blob: a work with three
// stored formats is exactly the case this command exists for.
func findOrphanCovers(ctx context.Context, bs blob.Store) (orphans []orphanCover, scanned int, err error) {
	// A missing grain and a grain with no cover both yield an empty claim, and
	// they mean different things to an operator, so the presence of the grain is
	// carried alongside rather than inferred from the empty string.
	type claim struct {
		hasGrain bool
		cover    string
	}
	claims := map[string]claim{}

	for entry, ierr := range bs.List(ctx, coverPrefix) {
		if ierr != nil {
			return nil, scanned, ierr
		}
		scanned++
		workID, ok := coverWorkOf(entry.Path)
		if !ok {
			orphans = append(orphans, orphanCover{Path: entry.Path, Reason: reasonBadPath, Size: entry.Size})
			continue
		}
		c, known := claims[workID]
		if !known {
			grain, _, gerr := bs.Get(ctx, bibframe.GrainPath(workID))
			switch {
			case errors.Is(gerr, blob.ErrNotFound):
				c = claim{}
			case gerr != nil:
				return nil, scanned, fmt.Errorf("read grain %s: %w", workID, gerr)
			default:
				cover, cerr := bibframe.CoverOf(grain, workID)
				if cerr != nil {
					return nil, scanned, fmt.Errorf("parse grain %s: %w", workID, cerr)
				}
				c = claim{hasGrain: true, cover: cover}
			}
			claims[workID] = c
		}
		switch {
		case !c.hasGrain:
			orphans = append(orphans, orphanCover{Path: entry.Path, WorkID: workID, Reason: reasonNoWork, Size: entry.Size})
		case c.cover == "":
			orphans = append(orphans, orphanCover{Path: entry.Path, WorkID: workID, Reason: reasonNoCover, Size: entry.Size})
		case c.cover != coverURLOf(entry.Path):
			orphans = append(orphans, orphanCover{
				Path: entry.Path, WorkID: workID, Reason: reasonStaleFormat,
				Referenced: c.cover, Size: entry.Size,
			})
		}
	}
	return orphans, scanned, nil
}

// coverWorkOf recovers the Work id from a cover blob path, or reports that the
// path is not one CoverBlobPath would have written.
func coverWorkOf(p string) (string, bool) {
	rest, ok := strings.CutPrefix(p, coverPrefix)
	if !ok {
		return "", false
	}
	shard, file, ok := strings.Cut(rest, "/")
	if !ok || strings.Contains(file, "/") {
		return "", false
	}
	ext := strings.TrimPrefix(path.Ext(file), ".")
	workID := strings.TrimSuffix(file, path.Ext(file))
	if !coverWorkID.MatchString(workID) || !isCoverExt(ext) {
		return "", false
	}
	// The shard is derived from the id, so a mismatch is a blob no reader would
	// ever look for -- report it rather than reap what it happens to name.
	if shard != workID[:min(2, len(workID))] {
		return "", false
	}
	return workID, true
}

// coverURLOf is the cover URL a grain would carry for this blob.
func coverURLOf(p string) string {
	return "covers/" + path.Base(p)
}

func isCoverExt(ext string) bool {
	switch ext {
	case "jpg", "png", "webp":
		return true
	}
	return false
}
