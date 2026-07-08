package ingest

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// RunStore is Run over a blob.Store (tasks/050): the same prior-load /
// resolve / cluster / build pipeline, with grains written under ETag
// optimistic concurrency so a concurrent editorial save is never clobbered
// -- the copy-cataloging commit path. Unchanged grains are not rewritten, so
// re-committing identical records is byte-stable and touches nothing. It
// does not write catalog.nq: store-backed deployments regenerate the bulk
// artifacts from grains via the rebuild trigger. Returns the changed (or
// removed) grain paths for that trigger.
func RunStore(ctx context.Context, prov Provider, st blob.Store, prefix string) (Result, []string, error) {
	if prov.Role() != RoleIngest {
		return Result{}, nil, fmt.Errorf("ingest: provider %q has role %s, not ingest", prov.Name(), prov.Role())
	}
	feed := prov.Name()
	recs, err := prov.Records(ctx)
	if err != nil {
		return Result{}, nil, fmt.Errorf("provider %q records: %w", feed, err)
	}
	prior, etags, err := bibframe.LoadPriorStore(ctx, st, prefix+"data/works/", feed)
	if err != nil {
		return Result{}, nil, fmt.Errorf("load prior grains: %w", err)
	}
	works, res, r := cluster(recs, prior)

	var changed []string
	for _, wg := range works {
		grain, err := bibframe.BuildWorkGrain(wg, feed)
		if err != nil {
			return res, changed, fmt.Errorf("grain %s: %w", wg.WorkID, err)
		}
		path := prefix + bibframe.GrainPath(wg.WorkID)
		opts := blob.PutOptions{ContentType: "application/n-quads"}
		if etag, ok := etags[path]; ok {
			old, _, err := st.Get(ctx, path)
			if err == nil && bytes.Equal(old, grain) {
				res.Stats.Grains++
				res.Stats.Records += len(wg.Instances)
				continue // byte-identical: nothing to write
			}
			opts.IfMatch = etag
		} else {
			opts.IfNoneMatch = true
		}
		if _, err := st.Put(ctx, path, grain, opts); err != nil {
			if errors.Is(err, blob.ErrPreconditionFailed) {
				return res, changed, fmt.Errorf("ingest: %s changed during the run; retry: %w", path, err)
			}
			return res, changed, err
		}
		res.Stats.Grains++
		res.Stats.Records += len(wg.Instances)
		changed = append(changed, path)
	}

	for _, id := range bibframe.RetiredWorks(prior.Merges) {
		path := prefix + bibframe.GrainPath(id)
		err := st.Delete(ctx, path)
		if errors.Is(err, blob.ErrNotFound) {
			continue
		}
		if err != nil {
			return res, changed, fmt.Errorf("remove retired grain %s: %w", id, err)
		}
		res.Retired++
		changed = append(changed, path)
	}
	res.Conflicts = r.Conflicts()
	return res, changed, nil
}
