// Package openlibrary is an offline external-identity enricher: it
// links a Work to its OpenLibrary work id by exact ISBN match, from a
// pre-downloaded ISBN -> work index (OpenLibrary publishes bulk dumps, so there is
// no per-record live API on the ingest path). The OpenLibrary URI is attached as an
// owl:sameAs outward link in the enrichment graph -- the minted `w…` id stays the
// primary identity.
//
// Matching is deliberately conservative: only an exact ISBN hit counts, and a Work
// whose ISBNs resolve to more than one distinct OpenLibrary work is left unlinked
// rather than guessed, so an ambiguous edition cluster never mints a wrong sameAs.
package openlibrary

import (
	"context"

	"github.com/freeeve/libcat/ingest"
)

// Name is the enricher's registry key and enrichment-graph name.
const Name = "openlibrary"

// Scheme labels the emitted external identities.
const Scheme = "openlibrary"

// Enricher matches Works against an ISBN -> OpenLibrary-work-URI index.
type Enricher struct {
	// index maps a normalized ISBN to an OpenLibrary work URI, e.g.
	// "https://openlibrary.org/works/OL45804W".
	index map[string]string
}

// New builds an Enricher over an ISBN -> OpenLibrary work URI index, normalizing
// its keys. The index is the offline dump's product; how the dump is obtained and
// read into this map is a separate concern (a follow-up wires the bulk-dump
// reader), which keeps the matching logic testable without a multi-GB fixture.
func New(index map[string]string) *Enricher {
	norm := make(map[string]string, len(index))
	for isbn, uri := range index {
		if k := NormalizeISBN(isbn); k != "" && uri != "" {
			norm[k] = uri
		}
	}
	return &Enricher{index: norm}
}

// Name implements ingest.Enricher.
func (e *Enricher) Name() string { return Name }

// Enrich implements ingest.Enricher: for each Work, the distinct OpenLibrary works
// its ISBNs resolve to are collected; a single distinct hit becomes an owl:sameAs
// identity, a conflict (more than one) is skipped, and no hit leaves the Work
// untouched. Confidence is 1 -- an ISBN match is exact, not scored. The pass is
// idempotent: RunEnrich drop-and-replaces the enrichment graph each run.
func (e *Enricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	var out []ingest.Enrichment
	for _, w := range works {
		hits := map[string]bool{}
		for _, isbn := range w.ISBNs {
			if uri, ok := e.index[NormalizeISBN(isbn)]; ok {
				hits[uri] = true
			}
		}
		if len(hits) != 1 {
			continue // no match, or an ambiguous cluster -> never guess
		}
		out = append(out, ingest.Enrichment{
			WorkID:     w.WorkID,
			Identities: []ingest.ExternalIdentity{{URI: onlyKey(hits), Scheme: Scheme}},
			Confidence: 1,
		})
	}
	return out, nil
}

func onlyKey(m map[string]bool) string {
	for k := range m {
		return k
	}
	return ""
}

// NormalizeISBN keeps only the ISBN's digits (and an X check digit, upper-cased),
// so the index and a Work's ISBNs compare on the same form regardless of hyphens or
// spaces. It deliberately does NOT convert between ISBN-10 and ISBN-13: the dump and
// the catalog should each carry whatever forms they have, and inventing a conversion
// here would risk a false match.
func NormalizeISBN(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			b = append(b, c)
		case c == 'x' || c == 'X':
			b = append(b, 'X')
		}
	}
	return string(b)
}
