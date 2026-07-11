// Package lchub is an offline external-identity enricher: it links a
// Work to its Library of Congress bf:Hub (id.loc.gov/resources/hubs) by an exact
// creator+title+language access-point match, from a pre-downloaded index (LC
// publishes the hubs as a bulk download; no live API on the ingest path). The Hub
// URI is attached as an owl:sameAs outward link -- the minted `w…` id stays primary.
//
// Matching reuses libcat's own clustering discipline (identity.WorkKey): normalized
// primary-creator + main-title + language. Language is the corroborating signal that
// keeps a bare title collision (two different works sharing an author and title) from
// linking. A Work with no usable access point (no title) is left unlinked, never
// guessed.
package lchub

import (
	"context"

	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
)

// Name is the enricher's registry key and enrichment-graph name.
const Name = "lchub"

// Scheme labels the emitted external identities.
const Scheme = "lchub"

// KeyFor is the access-point clustering key a Work is matched by: its primary
// creator, main title and language, folded through identity.WorkKey. The dump
// reader keys the index by the SAME function so both sides compare on one form.
// Returns "" for a Work with no usable title (no access point to match on).
func KeyFor(s ingest.WorkSummary) string {
	var author, lang string
	if len(s.Contributors) > 0 {
		author = s.Contributors[0] // the projector orders the primary creator first
	}
	if len(s.Languages) > 0 {
		lang = s.Languages[0]
	}
	return identity.WorkKey(author, s.Title, lang)
}

// Enricher matches Works against a WorkKey -> LC Hub URI index.
type Enricher struct {
	index map[string]string
}

// New builds an Enricher over a WorkKey -> Hub URI index (the offline dump's
// product; the bulk-dump reader is a separate concern, so the matching stays
// testable without the real download).
func New(index map[string]string) *Enricher {
	return &Enricher{index: index}
}

// Name implements ingest.Enricher.
func (e *Enricher) Name() string { return Name }

// Enrich implements ingest.Enricher: each Work with a title is keyed by its
// access point and, on an exact index hit, linked to its Hub with owl:sameAs.
// A title-less Work, or one whose key the index does not carry, is left untouched.
// Confidence is 1 -- an exact access-point match is not scored. Idempotent via
// RunEnrich.
func (e *Enricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	var out []ingest.Enrichment
	for _, w := range works {
		key := KeyFor(w)
		if key == "" {
			continue
		}
		uri, ok := e.index[key]
		if !ok {
			continue
		}
		out = append(out, ingest.Enrichment{
			WorkID:     w.WorkID,
			Identities: []ingest.ExternalIdentity{{URI: uri, Scheme: Scheme}},
			Confidence: 1,
		})
	}
	return out, nil
}
