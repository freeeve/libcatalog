// Sidecar index builder (tasks/167): serializes one scheme's terms into
// range-servable roaringrange artifacts so the server never materializes a
// big vocabulary as Go maps. The on-disk layout, the manifest shape and the
// remove/orphan lifecycle live in storage/vocabsidecar (root); this file is the
// builder that writes into that layout.
//
// Doc ids are the scheme's term URIs in sorted order, so RRIL postings for
// one key surface the smallest URI first and output is deterministic.
package vocab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"github.com/RoaringBitmap/roaring/v2"
	rr "github.com/freeeve/roaringrange"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/storage/blob"
	"github.com/freeeve/libcat/storage/vocabsidecar"
)

const identifierTiers = 3

// BuildSidecar builds and stores the sidecar artifacts for scheme from the
// installed snapshot at source (usually <prefix>vocab/<scheme>.nq). It
// parses the snapshot with the same routing the map loader uses, so the two
// paths index identical terms.
func BuildSidecar(ctx context.Context, st blob.Store, prefix, scheme, source string) (*vocabsidecar.SidecarManifest, error) {
	data, etag, err := st.Get(ctx, source)
	if err != nil {
		return nil, fmt.Errorf("vocab: sidecar source %s: %w", source, err)
	}
	ds, err := rdf.ParseNQuads(data)
	if err != nil {
		return nil, fmt.Errorf("vocab: parse %s: %w", source, err)
	}
	tmp := &snapshot{schemes: map[string]map[string]*Term{}, search: map[string][]searchEntry{}}
	tmp.addDataset(ds, nil)
	tmp.finish()
	byURI := tmp.schemes[scheme]
	if len(byURI) == 0 {
		return nil, fmt.Errorf("vocab: %s carries no authority:%s terms", source, scheme)
	}
	sourceSchemes := make([]string, 0, len(tmp.schemes))
	for s := range tmp.schemes {
		sourceSchemes = append(sourceSchemes, s)
	}
	sort.Strings(sourceSchemes)
	return buildSidecarTerms(ctx, st, prefix, scheme, source, etag, sourceSchemes, byURI, tmp.search[scheme])
}

func buildSidecarTerms(ctx context.Context, st blob.Store, prefix, scheme, source, sourceETag string, sourceSchemes []string, byURI map[string]*Term, search []searchEntry) (*vocabsidecar.SidecarManifest, error) {
	uris := make([]string, 0, len(byURI))
	for uri := range byURI {
		uris = append(uris, uri)
	}
	sort.Strings(uris)
	doc := make(map[string]uint32, len(uris))
	for i, uri := range uris {
		doc[uri] = uint32(i)
	}

	// Records: full Term JSON in doc order.
	records := make([][]byte, len(uris))
	live := 0
	for i, uri := range uris {
		t := byURI[uri]
		if t.MergedInto == "" {
			live++
		}
		data, err := json.Marshal(t)
		if err != nil {
			return nil, fmt.Errorf("vocab: marshal %s: %w", uri, err)
		}
		records[i] = data
	}
	var bin, idx bytes.Buffer
	if err := rr.WriteRecords(&bin, &idx, records); err != nil {
		return nil, fmt.Errorf("vocab: write records: %w", err)
	}

	// URI lookup: every term, retired included (Lookup resolves them).
	uriEntries := make([]rr.LookupEntry, len(uris))
	for i, uri := range uris {
		uriEntries[i] = rr.LookupEntry{ID: uri, Doc: uint32(i)}
	}
	uriBuf := &bytes.Buffer{}
	if err := rr.WriteLookup(uriBuf, uriEntries); err != nil {
		return nil, fmt.Errorf("vocab: write uri lookup: %w", err)
	}

	// Identifier tiers, live terms only, canonicalized like buildMatch.
	tierIDs := func(t *Term, tier int) []string {
		switch tier {
		case 0:
			return []string{t.ID}
		case 1:
			return t.ExactMatch
		default:
			return t.CloseMatch
		}
	}
	tierBufs := make([]*bytes.Buffer, identifierTiers)
	for k := range identifierTiers {
		var entries []rr.LookupEntry
		for _, uri := range uris {
			t := byURI[uri]
			if t.MergedInto != "" {
				continue
			}
			for _, id := range tierIDs(t, k) {
				if key := canonIdentifier(id); key != "" {
					entries = append(entries, rr.LookupEntry{ID: key, Doc: doc[t.ID]})
				}
			}
		}
		tierBufs[k] = &bytes.Buffer{}
		if err := rr.WriteLookup(tierBufs[k], entries); err != nil {
			return nil, fmt.Errorf("vocab: write id tier %d: %w", k+1, err)
		}
	}

	searchBuf, err := encodeSearch(search, doc)
	if err != nil {
		return nil, err
	}

	puts := []struct {
		suffix string
		data   []byte
	}{
		{".rrsr.bin", bin.Bytes()},
		{".rrsr.idx", idx.Bytes()},
		{".uri.rril", uriBuf.Bytes()},
		{".id1.rril", tierBufs[0].Bytes()},
		{".id2.rril", tierBufs[1].Bytes()},
		{".id3.rril", tierBufs[2].Bytes()},
		{".search.rrt", searchBuf},
	}
	for _, p := range puts {
		if _, err := st.Put(ctx, vocabsidecar.Path(prefix, scheme, p.suffix), p.data, blob.PutOptions{}); err != nil {
			return nil, fmt.Errorf("vocab: put sidecar %s: %w", p.suffix, err)
		}
	}
	// Best-effort removal of the pre-v2 LCVS search blob a rebuild orphans.
	if err := st.Delete(ctx, vocabsidecar.Path(prefix, scheme, ".search.bin")); err != nil && !errors.Is(err, blob.ErrNotFound) {
		slog.Warn("vocab: could not remove legacy search blob", "scheme", scheme, "err", err)
	}
	m := &vocabsidecar.SidecarManifest{
		Version:       vocabsidecar.Version,
		Scheme:        scheme,
		Source:        source,
		SourceETag:    sourceETag,
		SourceSchemes: sourceSchemes,
		Terms:         len(uris),
		Live:          live,
	}
	mdata, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	// The manifest lands last: its presence implies a complete artifact set.
	if _, err := st.Put(ctx, vocabsidecar.Path(prefix, scheme, vocabsidecar.ManifestSuffix), mdata, blob.PutOptions{}); err != nil {
		return nil, fmt.Errorf("vocab: put sidecar manifest: %w", err)
	}
	return m, nil
}

// encodeSearch serializes search entries as an RRTI term index (tasks/169):
// each normalized label is a dictionary term whose posting carries doc<<1|alt
// encoded IDs, so a prefix query range-reads only the dict blocks spanning
// the prefix plus the matched labels' postings -- nothing but the router FST
// stays resident. Norms are pre-normalized, so the index is written
// case-sensitive (no query-side folding) with no language filters; the head
// boundary clears every encoded ID, so a posting is one head read.
func encodeSearch(entries []searchEntry, doc map[string]uint32) ([]byte, error) {
	postings := make(map[string]*roaring.Bitmap, len(entries))
	var maxEnc uint32
	for _, e := range entries {
		d, ok := doc[e.uri]
		if !ok {
			return nil, fmt.Errorf("vocab: search entry uri %s has no doc", e.uri)
		}
		enc := d << 1
		if e.alt {
			enc |= 1
		}
		bm := postings[e.norm]
		if bm == nil {
			bm = roaring.New()
			postings[e.norm] = bm
		}
		bm.Add(enc)
		maxEnc = max(maxEnc, enc)
	}
	out := &bytes.Buffer{}
	if err := rr.WriteTermIndexFull(out, postings, (maxEnc>>16+1)<<16, rr.TermLanguageNone, false, false, false, 0); err != nil {
		return nil, fmt.Errorf("vocab: write search index: %w", err)
	}
	return out.Bytes(), nil
}
