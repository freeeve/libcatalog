package httpapi

import (
	"context"
	"sort"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"

	"github.com/freeeve/libcat/backend/workindex"
)

// duplicateView reports that a saved (or previewed) doc's identity collides
// with a different existing work (tasks/068) -- the editor's non-blocking
// pre-save warning, mirroring the copycat match banner.
type duplicateView struct {
	WorkID string `json:"workId"`
	// Via names the colliding signal: a shared identifier (ISBN, provider
	// id) or the author/title/language clustering key.
	Via string `json:"via"` // "identifier" | "title-author"
}

// findDuplicate checks the edited grain's identity signals against the shared
// work index: identifier collisions outrank clustering-key collisions,
// matches from the work's own grain never count (post-merge grains hold
// sibling works), and any error degrades to "no warning" -- the save itself
// is never blocked or failed by this check.
func findDuplicate(ctx context.Context, ix *workindex.Index, workID string, grain []byte) *duplicateView {
	self, err := identity.ScanGrain(grain)
	if err != nil {
		return nil
	}
	var selfKeys, selfProv []string
	for _, w := range self.Works {
		if w.WorkID == workID && w.ClusterKey != "" {
			selfKeys = append(selfKeys, w.ClusterKey)
		}
	}
	for _, inst := range self.Instances {
		if inst.WorkID != workID {
			continue
		}
		selfProv = append(selfProv, inst.ProviderKeys...)
	}
	if len(selfKeys) == 0 && len(selfProv) == 0 {
		return nil
	}
	sort.Strings(selfKeys)
	sort.Strings(selfProv)
	selfPath := bibframe.GrainPath(workID)
	for _, pk := range selfProv {
		owners, err := ix.ProviderOwners(ctx, pk)
		if err != nil {
			return nil
		}
		for _, ref := range owners {
			if ref.Path != selfPath {
				return &duplicateView{WorkID: ref.WorkID, Via: "identifier"}
			}
		}
	}
	for _, ck := range selfKeys {
		owners, err := ix.ClusterOwners(ctx, ck)
		if err != nil {
			return nil
		}
		for _, ref := range owners {
			if ref.Path != selfPath {
				return &duplicateView{WorkID: ref.WorkID, Via: "title-author"}
			}
		}
	}
	return nil
}
