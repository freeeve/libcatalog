package vocabsrc

import (
	"context"
	"strings"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/ingest"

	"github.com/freeeve/libcat/backend/vocab"
)

// Enricher adapts a suggest-capable source to the ingest.Enricher contract
// (the generalized locsh shape): a Work's uncontrolled tags reconcile against
// the source's typeahead API, exact normalized-label matches score 1.0 and
// prefix matches 0.6, and candidates at or above MinConfidence become
// controlled-subject enrichments.
type Enricher struct {
	Src    Source
	Client *SuggestClient
	// Index, when set, upgrades each match with the locally-installed term
	// description (multilingual labels, broader edges) and rides its
	// skos:broader ancestor chain along as Enrichment.Terms;
	// a match whose scheme is not installed keeps the suggest-API label.
	Index *vocab.Index
	// MinConfidence drops weaker matches. Default 0.9 (exact label only).
	MinConfidence float64
}

// NewEnricher wraps a registry source as an enrichment provider.
func NewEnricher(src Source, client *SuggestClient) *Enricher {
	return &Enricher{Src: src, Client: client, MinConfidence: 0.9}
}

// Name implements ingest.Enricher; the source name keys the enrichment graph.
func (e *Enricher) Name() string { return e.Src.Name }

func (e *Enricher) minConfidence() float64 {
	if e.MinConfidence <= 0 {
		return 0.9
	}
	return e.MinConfidence
}

// Enrich implements ingest.Enricher: each distinct tag is looked up once per
// run, and matches at or above MinConfidence become subject candidates on
// every Work carrying the tag.
func (e *Enricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	type match struct {
		sugg       Suggestion
		confidence float64
	}
	cache := map[string]*match{}
	var out []ingest.Enrichment
	for _, work := range works {
		seen := map[string]bool{}
		enrichment := ingest.Enrichment{WorkID: work.WorkID, Confidence: 1}
		for _, tag := range work.Tags {
			key := normalizeTag(tag)
			if key == "" {
				continue
			}
			hit, ok := cache[key]
			if !ok {
				suggs, err := e.Client.Suggest(ctx, e.Src, key, 5)
				if err != nil {
					return nil, err
				}
				for _, sugg := range suggs {
					switch {
					case normalizeTag(sugg.Label) == key:
						hit = &match{sugg: sugg, confidence: 1.0}
					case strings.HasPrefix(normalizeTag(sugg.Label), key) && hit == nil:
						hit = &match{sugg: sugg, confidence: 0.6}
					}
					if hit != nil && hit.confidence == 1.0 {
						break
					}
				}
				cache[key] = hit
			}
			if hit == nil || hit.confidence < e.minConfidence() || seen[hit.sugg.ID] {
				continue
			}
			seen[hit.sugg.ID] = true
			subj := bibframe.AuthoritySubject{
				URI:    hit.sugg.ID,
				Labels: map[string]string{"en": hit.sugg.Label},
			}
			if e.Index != nil {
				if t, ok := e.Index.Lookup(hit.sugg.Scheme, hit.sugg.ID); ok {
					subj = bibframe.AuthoritySubject{URI: t.ID, Labels: t.Labels, Broader: t.Broader}
					for _, a := range e.Index.Ancestors(hit.sugg.Scheme, t.ID) {
						if seen[a.ID] {
							continue
						}
						seen[a.ID] = true
						enrichment.Terms = append(enrichment.Terms, bibframe.AuthoritySubject{
							URI: a.ID, Labels: a.Labels, Broader: a.Broader,
						})
					}
				}
			}
			enrichment.Subjects = append(enrichment.Subjects, subj)
			if hit.confidence < enrichment.Confidence {
				enrichment.Confidence = hit.confidence
			}
		}
		if len(enrichment.Subjects) > 0 {
			out = append(out, enrichment)
		}
	}
	return out, nil
}

// normalizeTag canonicalizes a tag for matching: lowercase, whitespace
// collapsed, trailing periods (MARC-ism) stripped.
func normalizeTag(tag string) string {
	s := strings.Join(strings.Fields(strings.ToLower(tag)), " ")
	return strings.TrimSuffix(s, ".")
}
