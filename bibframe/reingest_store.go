package bibframe

import (
	"context"
	"fmt"
	"path"

	"github.com/freeeve/libcat/storage/blob"
)

// LoadPriorStore is LoadPrior over a blob.Store: it reads every per-Work
// grain (*.nq, skipping the bulk catalog.nq) under prefix, returning the
// recovered Prior plus each grain's ETag keyed by path. The ETags feed
// conditional writes: a re-ingest that read a grain at etag E writes it back
// with IfMatch E, so an editorial publish landing mid-ingest surfaces as
// ErrPreconditionFailed instead of being clobbered (the writer re-reads,
// re-extracts non-feed graphs, unions, and retries). An empty tree (a first
// build) yields empty state and no error.
func LoadPriorStore(ctx context.Context, st blob.Store, prefix, provider string) (Prior, map[string]string, error) {
	prior := Prior{Editorial: map[string][]byte{}}
	etags := map[string]string{}
	feed := FeedGraph(provider)
	for entry, err := range st.List(ctx, prefix) {
		if err != nil {
			return Prior{}, nil, fmt.Errorf("list grains: %w", err)
		}
		if !isWorkGrainName(path.Base(entry.Path)) {
			continue
		}
		b, etag, err := st.Get(ctx, entry.Path)
		if err != nil {
			return Prior{}, nil, fmt.Errorf("%s: %w", entry.Path, err)
		}
		etags[entry.Path] = etag
		if err := prior.accumulateGrain(b, feed); err != nil {
			return Prior{}, nil, fmt.Errorf("%s: %w", entry.Path, err)
		}
	}
	return prior, etags, nil
}
