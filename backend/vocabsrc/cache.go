package vocabsrc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// CacheTerm writes a live pick's minimal term description (prefLabel,
// definition, exactMatch siblings) into the authorities tree under
// cache/<scheme>/ and swaps the index (tasks/072). A subject picked from a
// live source labels forever -- across saves and restarts -- and its
// exactMatch links join the crosswalk data. Re-caching an already-cached
// term is a no-op.
func (s *Service) CacheTerm(ctx context.Context, sugg Suggestion) error {
	if sugg.Scheme == "" || sugg.Label == "" ||
		(!strings.HasPrefix(sugg.ID, "http://") && !strings.HasPrefix(sugg.ID, "https://")) {
		return fmt.Errorf("%w: caching needs a scheme, an http(s) id, and a label", ErrValidation)
	}
	term := bibframe.AuthorityTerm{
		URI:        sugg.ID,
		PrefLabel:  map[string]string{"en": sugg.Label},
		ExactMatch: sugg.ExactMatch,
	}
	if sugg.Description != "" {
		term.Definition = map[string]string{"en": sugg.Description}
	}
	if len(sugg.Variants) > 0 {
		term.AltLabel = map[string][]string{"en": sugg.Variants}
	}
	quads, err := term.Quads()
	if err != nil {
		return err
	}
	graph := bibframe.AuthorityGraph(sugg.Scheme)
	var enc rdf.Encoder
	var out []byte
	for _, q := range quads {
		q.G = graph
		out = enc.AppendQuad(out, q)
	}
	_, err = s.Blob.Put(ctx, s.cachePath(sugg.Scheme, sugg.ID), out, blob.PutOptions{
		IfNoneMatch: true, ContentType: "application/n-quads",
	})
	if errors.Is(err, blob.ErrPreconditionFailed) {
		return nil // already cached; the index has it
	}
	if err != nil {
		return err
	}
	return s.Reload(ctx)
}

func (s *Service) cachePath(scheme, id string) string {
	sum := sha256.Sum256([]byte(id))
	return s.prefix() + "cache/" + scheme + "/" + hex.EncodeToString(sum[:8]) + ".nq"
}

// cachedSchemes lists the schemes with cached live picks, so a configured
// scheme filter never drops them at reload.
func (s *Service) cachedSchemes(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	prefix := s.prefix() + "cache/"
	for entry, err := range s.Blob.List(ctx, prefix) {
		if err != nil {
			return nil, err
		}
		rest := strings.TrimPrefix(entry.Path, prefix)
		if scheme, _, ok := strings.Cut(rest, "/"); ok && scheme != "" {
			seen[scheme] = true
		}
	}
	out := make([]string, 0, len(seen))
	for scheme := range seen {
		out = append(out, scheme)
	}
	return out, nil
}
