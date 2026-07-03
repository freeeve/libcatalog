package httpapi

import (
	"context"
	"path"
	"strings"

	"github.com/freeeve/libcatalog/identity"
	"github.com/freeeve/libcatalog/storage/blob"
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

// findDuplicate dry-runs the edited grain's identity signals against every
// other work in the corpus: identifier collisions outrank clustering-key
// collisions, and any error degrades to "no warning" -- the save itself is
// never blocked or failed by this check.
func findDuplicate(ctx context.Context, bs blob.Store, workID string, grain []byte) *duplicateView {
	self, err := identity.ScanGrain(grain)
	if err != nil {
		return nil
	}
	selfKeys := map[string]bool{}
	selfProv := map[string]bool{}
	for _, w := range self.Works {
		if w.WorkID == workID && w.ClusterKey != "" {
			selfKeys[w.ClusterKey] = true
		}
	}
	for _, inst := range self.Instances {
		if inst.WorkID != workID {
			continue
		}
		for _, pk := range inst.ProviderKeys {
			selfProv[pk] = true
		}
	}
	if len(selfKeys) == 0 && len(selfProv) == 0 {
		return nil
	}
	var titleHit *duplicateView
	for entry, err := range bs.List(ctx, "data/works/") {
		if err != nil {
			return titleHit
		}
		base := path.Base(entry.Path)
		if !strings.HasSuffix(base, ".nq") || base == "catalog.nq" || base == workID+".nq" {
			continue
		}
		other, _, err := bs.Get(ctx, entry.Path)
		if err != nil {
			continue
		}
		gi, err := identity.ScanGrain(other)
		if err != nil {
			continue
		}
		for _, inst := range gi.Instances {
			for _, pk := range inst.ProviderKeys {
				if selfProv[pk] && inst.WorkID != "" {
					return &duplicateView{WorkID: inst.WorkID, Via: "identifier"}
				}
			}
		}
		if titleHit == nil {
			for _, w := range gi.Works {
				if w.ClusterKey != "" && selfKeys[w.ClusterKey] {
					titleHit = &duplicateView{WorkID: w.WorkID, Via: "title-author"}
				}
			}
		}
	}
	return titleHit
}
