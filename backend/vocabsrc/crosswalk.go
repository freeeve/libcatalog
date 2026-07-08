package vocabsrc

import (
	"context"
	"slices"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"

	"github.com/freeeve/libcat/backend/vocab"
)

// Crosswalk confidence by link kind: exactMatch is definitional identity,
// closeMatch (how homosaurus links most LCSH headings) is near-equivalence.
const (
	confExactMatch = 1.0
	confCloseMatch = 0.85
)

// CrosswalkEnricher walks every work subject's skos:exactMatch/closeMatch
// links into one target scheme (tasks/072): a work carrying a homosaurus
// heading whose term matches an LCSH heading gains the LCSH equivalent as a
// moderated suggestion. Purely local -- both vocabularies must be loaded in
// the index; no network.
type CrosswalkEnricher struct {
	Index *vocab.Index
	// Target is the scheme equivalents resolve into.
	Target string
}

// NewCrosswalk wraps the index as a crosswalk enrichment source for one
// target scheme.
func NewCrosswalk(ix *vocab.Index, target string) *CrosswalkEnricher {
	return &CrosswalkEnricher{Index: ix, Target: target}
}

// Name implements ingest.Enricher; the registry key and enrichment graph.
func (e *CrosswalkEnricher) Name() string { return "crosswalk-" + e.Target }

// Enrich implements ingest.Enricher: for each work, every controlled
// subject's match links resolving in the target scheme (and not already on
// the work) become subject candidates, and each candidate's skos:broader
// ancestor chain rides along as standalone term metadata (tasks/178) so
// hierarchy nodes stay labeled without a work carrying them.
func (e *CrosswalkEnricher) Enrich(_ context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	var out []ingest.Enrichment
	for _, work := range works {
		enrichment := ingest.Enrichment{WorkID: work.WorkID, Confidence: 1}
		seen := map[string]bool{}
		for _, uri := range work.Subjects {
			term, ok := e.Index.Resolve(uri)
			if !ok || term.Scheme == e.Target {
				continue
			}
			addMatches := func(links []string, confidence float64) {
				for _, m := range links {
					if seen[m] || slices.Contains(work.Subjects, m) {
						continue
					}
					target, ok := e.Index.Lookup(e.Target, m)
					if !ok || target.MergedInto != "" {
						continue
					}
					seen[m] = true
					enrichment.Subjects = append(enrichment.Subjects, bibframe.AuthoritySubject{
						URI: target.ID, Labels: target.Labels, Broader: target.Broader,
					})
					for _, a := range e.Index.Ancestors(e.Target, target.ID) {
						if seen[a.ID] {
							continue
						}
						seen[a.ID] = true
						enrichment.Terms = append(enrichment.Terms, bibframe.AuthoritySubject{
							URI: a.ID, Labels: a.Labels, Broader: a.Broader,
						})
					}
					if confidence < enrichment.Confidence {
						enrichment.Confidence = confidence
					}
				}
			}
			addMatches(term.ExactMatch, confExactMatch)
			addMatches(term.CloseMatch, confCloseMatch)
		}
		if len(enrichment.Subjects) > 0 {
			out = append(out, enrichment)
		}
	}
	return out, nil
}
