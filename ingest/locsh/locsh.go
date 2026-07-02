// Package locsh is the reference enrichment provider: it reconciles a Work's
// uncontrolled tags (feed genres, approved folksonomy) against Library of
// Congress Subject Headings via the public id.loc.gov suggest2 API, yielding
// controlled-subject candidates with label-match confidence. It demonstrates
// the Enricher shape for network reconciliation sources; deployments choose
// queue (moderated) or direct (auto-approve) execution.
package locsh

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/ingest"
)

// Name is the enricher's registry key and enrichment-graph name.
const Name = "locsh"

// Enricher reconciles tags against id.loc.gov.
type Enricher struct {
	// BaseURL overrides the id.loc.gov root (tests). Default production.
	BaseURL string
	// Client overrides the HTTP client. Default http.DefaultClient.
	Client *http.Client
	// MinConfidence drops weaker matches. Default 0.9 (exact label only).
	MinConfidence float64
}

// New returns a production-configured Enricher.
func New() *Enricher {
	return &Enricher{BaseURL: "https://id.loc.gov", MinConfidence: 0.9}
}

// Name implements ingest.Enricher.
func (e *Enricher) Name() string { return Name }

// suggest2 response subset.
type suggestResponse struct {
	Hits []struct {
		URI          string `json:"uri"`
		ALabel       string `json:"aLabel"`
		SuggestLabel string `json:"suggestLabel"`
	} `json:"hits"`
}

// Enrich implements ingest.Enricher: each distinct tag is looked up once per
// run (a shared cache across the batch), and matches at or above
// MinConfidence become subject candidates on every Work carrying the tag.
func (e *Enricher) Enrich(ctx context.Context, works []ingest.WorkSummary) ([]ingest.Enrichment, error) {
	cache := map[string]*matched{}
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
				var err error
				hit, err = e.lookup(ctx, key)
				if err != nil {
					return nil, err
				}
				cache[key] = hit
			}
			if hit == nil || hit.confidence < e.minConfidence() || seen[hit.uri] {
				continue
			}
			seen[hit.uri] = true
			enrichment.Subjects = append(enrichment.Subjects, bibframe.AuthoritySubject{
				URI:    hit.uri,
				Labels: map[string]string{"en": hit.label},
			})
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

type matched struct {
	uri        string
	label      string
	confidence float64
}

func (e *Enricher) minConfidence() float64 {
	if e.MinConfidence <= 0 {
		return 0.9
	}
	return e.MinConfidence
}

// lookup queries suggest2 for one normalized tag; nil = no usable hit.
func (e *Enricher) lookup(ctx context.Context, tag string) (*matched, error) {
	base := e.BaseURL
	if base == "" {
		base = "https://id.loc.gov"
	}
	client := e.Client
	if client == nil {
		client = http.DefaultClient
	}
	u := fmt.Sprintf("%s/authorities/subjects/suggest2?q=%s&count=5", base, url.QueryEscape(tag))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("locsh: suggest %q: %w", tag, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("locsh: suggest %q: status %d", tag, resp.StatusCode)
	}
	var body suggestResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("locsh: suggest %q: %w", tag, err)
	}
	for _, hit := range body.Hits {
		label := hit.ALabel
		if label == "" {
			label = hit.SuggestLabel
		}
		if hit.URI == "" || label == "" {
			continue
		}
		switch {
		case normalizeTag(label) == tag:
			return &matched{uri: hit.URI, label: label, confidence: 1.0}, nil
		case strings.HasPrefix(normalizeTag(label), tag):
			return &matched{uri: hit.URI, label: label, confidence: 0.6}, nil
		}
	}
	return nil, nil
}

// normalizeTag canonicalizes a tag for matching: lowercase, whitespace
// collapsed, trailing periods (MARC-ism) stripped.
func normalizeTag(tag string) string {
	s := strings.Join(strings.Fields(strings.ToLower(tag)), " ")
	return strings.TrimSuffix(s, ".")
}
