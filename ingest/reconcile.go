package ingest

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
	"github.com/freeeve/libcodex/rdf"
)

// Reconciliation policies: what happens to a Work whose sole bib
// feed stopped listing it.
const (
	// ReconcileReview flags the Work (lcat:withdrawnFromFeed) for the admin
	// review queue and changes nothing else.
	ReconcileReview = "review"
	// ReconcileAutoSuppress flags and suppresses in one pass, recording the
	// pass as the suppressor so a returning title un-suppresses itself.
	ReconcileAutoSuppress = "auto-suppress"
)

// ReconcileResult counts what one pass did.
type ReconcileResult struct {
	// Flagged Works were newly marked withdrawn this pass.
	Flagged int
	// Suppressed Works were auto-suppressed this pass.
	Suppressed int
	// Cleared Works returned to the feed and lost their withdrawal flag.
	Cleared int
	// Unsuppressed Works returned and lost a reconcile-set suppression.
	Unsuppressed int
	// FlaggedIDs lists the newly withdrawn Works, sorted.
	FlaggedIDs []string
}

// Reconcile diffs the corpus against the Works a feed's latest scan resolved
// to (present) and flags the leavers: a Work is withdrawn only when this
// feed is its sole bib source and no curator has invested editorial
// statements in it -- items, tags, merges, or a keep decision all protect it.
// Returning Works lose the flag (and a reconcile-set suppression), so a
// title cycling out of and back into a collection round-trips with identity
// and editorial statements intact. Nothing is ever deleted.
func Reconcile(ctx context.Context, st blob.Store, prefix, feed string, present map[string]bool, policy, date string) (ReconcileResult, error) {
	var res ReconcileResult
	if policy != ReconcileReview && policy != ReconcileAutoSuppress {
		return res, fmt.Errorf("ingest: reconcile policy must be %s or %s", ReconcileReview, ReconcileAutoSuppress)
	}
	feedGraph := bibframe.FeedGraph(feed).Value
	for entry, err := range st.List(ctx, prefix+"data/works/") {
		if err != nil {
			return res, err
		}
		base := path.Base(entry.Path)
		if !strings.HasSuffix(base, ".nq") || base == "catalog.nq" {
			continue
		}
		grain, etag, err := st.Get(ctx, entry.Path)
		if err != nil {
			return res, err
		}
		next, err := reconcileGrain(grain, feedGraph, present, policy, date, &res)
		if err != nil {
			return res, fmt.Errorf("%s: %w", entry.Path, err)
		}
		if next == nil {
			continue
		}
		if _, err := st.Put(ctx, entry.Path, next, blob.PutOptions{ContentType: "application/n-quads", IfMatch: etag}); err != nil {
			return res, fmt.Errorf("reconcile %s: %w", entry.Path, err)
		}
	}
	sort.Strings(res.FlaggedIDs)
	return res, nil
}

// reconcileGrain applies the pass to one grain (post-merge grains can hold
// several Works) and returns the rewritten bytes, or nil when untouched.
func reconcileGrain(grain []byte, feedGraph string, present map[string]bool, policy, date string, res *ReconcileResult) ([]byte, error) {
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return nil, err
	}
	protected := grainProtected(ds, feedGraph)
	changed := false
	for _, workID := range grainWorkIDs(ds) {
		vis, err := bibframe.Visibility(grain, workID)
		if err != nil {
			return nil, err
		}
		if present[workID] {
			if vis.Withdrawn != "" {
				if grain, err = bibframe.ClearWithdrawn(grain, workID); err != nil {
					return nil, err
				}
				res.Cleared++
				changed = true
			}
			if vis.Suppressed && vis.SuppressedBy == bibframe.SuppressedByReconcile {
				if grain, err = bibframe.SetSuppressed(grain, workID, false); err != nil {
					return nil, err
				}
				res.Unsuppressed++
				changed = true
			}
			if vis.Kept {
				if grain, err = bibframe.SetFeedKept(grain, workID, false); err != nil {
					return nil, err
				}
				changed = true
			}
			continue
		}
		if protected || vis.Tombstoned || vis.Kept {
			continue
		}
		if vis.Withdrawn == "" {
			if grain, err = bibframe.SetWithdrawn(grain, workID, date); err != nil {
				return nil, err
			}
			res.Flagged++
			res.FlaggedIDs = append(res.FlaggedIDs, workID)
			changed = true
		}
		if policy == ReconcileAutoSuppress && !vis.Suppressed {
			if grain, err = bibframe.SetSuppressed(grain, workID, true); err != nil {
				return nil, err
			}
			if grain, err = bibframe.SetSuppressedBy(grain, workID, bibframe.SuppressedByReconcile); err != nil {
				return nil, err
			}
			res.Suppressed++
			changed = true
		}
	}
	if !changed {
		return nil, nil
	}
	return grain, nil
}

// grainWorkIDs lists the minted Works a grain holds.
func grainWorkIDs(ds *rdf.Dataset) []string {
	const rdfType = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
	const bfWork = "http://id.loc.gov/ontologies/bibframe/Work"
	seen := map[string]bool{}
	var out []string
	for _, q := range ds.Quads {
		if q.P.Value != rdfType || !q.O.IsIRI() || q.O.Value != bfWork || !q.S.IsIRI() {
			continue
		}
		v := q.S.Value
		if !strings.HasPrefix(v, "#") || !strings.HasSuffix(v, "Work") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(v, "#"), "Work")
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// grainProtected reports whether reconciliation must leave this grain's
// Works alone: bib statements from another feed, or editorial statements
// beyond the reconciliation/visibility marks themselves (items, tags,
// merges -- curator investment).
func grainProtected(ds *rdf.Dataset, feedGraph string) bool {
	editorial := bibframe.EditorialGraph().Value
	reconcilePreds := map[string]bool{
		bibframe.PredWithdrawn:    true,
		bibframe.PredSuppressed:   true,
		bibframe.PredSuppressedBy: true,
		bibframe.PredFeedKept:     true,
		bibframe.PredTombstoned:   true,
	}
	for _, q := range ds.Quads {
		g := q.G.Value
		if strings.HasPrefix(g, "feed:") && g != feedGraph {
			return true
		}
		if g == editorial && !reconcilePreds[q.P.Value] {
			return true
		}
	}
	return false
}
