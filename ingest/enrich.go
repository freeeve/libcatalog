package ingest

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/storage/blob"
)

// WorkSummary is the slice of a Work an enricher reasons over: enough for a
// vocabulary lookup or reconciliation call without handing over the graph.
type WorkSummary struct {
	WorkID       string
	Title        string
	Contributors []string
	ISBNs        []string
	// Tags are the Work's uncontrolled subject labels (feed genres,
	// approved folksonomy) -- the raw material tag-to-controlled-term
	// reconciliation enrichers match against.
	Tags []string
}

// Enrichment is one Work's enrichment result: controlled subjects to assert.
type Enrichment struct {
	WorkID   string
	Subjects []AuthoritySubject
	// Confidence (0-1] qualifies queue-moderated enrichments; direct-mode
	// callers may threshold on it.
	Confidence float64
}

// Enricher produces enrichments for batches of Works. This executes the
// RoleEnrich half of the provider model that Run reserves: enrichers never
// touch feed graphs -- their statements land in their own enrichment:<name>
// graph (direct mode) or in the moderation queue (a deployment decision made
// by the caller, not the enricher).
type Enricher interface {
	Name() string
	Enrich(ctx context.Context, works []WorkSummary) ([]Enrichment, error)
}

// enrichBatchSize bounds how many summaries one Enrich call receives.
const enrichBatchSize = 50

// RunEnrich executes an enricher in direct (auto-approve) mode over every
// grain under prefix in the store: each returned Work's enrichment:<name>
// graph is dropped and replaced with the fresh assertions, so a re-run is
// idempotent, and returning an Enrichment with no Subjects explicitly clears
// a Work's statements from this source. Works absent from the result are
// left untouched. Returns the number of Works written.
func RunEnrich(ctx context.Context, st blob.Store, prefix string, e Enricher) (int, error) {
	graph := bibframe.EnrichmentGraph(e.Name())
	summaries, paths, err := ScanSummaries(ctx, st, prefix)
	if err != nil {
		return 0, err
	}
	enriched := 0
	for start := 0; start < len(summaries); start += enrichBatchSize {
		end := min(start+enrichBatchSize, len(summaries))
		results, err := e.Enrich(ctx, summaries[start:end])
		if err != nil {
			return enriched, fmt.Errorf("enrich %s: %w", e.Name(), err)
		}
		for _, res := range results {
			grainPath, ok := paths[res.WorkID]
			if !ok {
				continue
			}
			quads := enrichmentQuads(res)
			if err := replaceGrainGraph(ctx, st, grainPath, graph, quads); err != nil {
				return enriched, fmt.Errorf("%s: %w", grainPath, err)
			}
			enriched++
		}
	}
	return enriched, nil
}

// enrichmentQuads renders one enrichment as self-contained statements: the
// subject links plus each term's labels and hierarchy, all destined for the
// enricher's own graph.
func enrichmentQuads(res Enrichment) []rdf.Quad {
	var quads []rdf.Quad
	for _, subj := range res.Subjects {
		quads = append(quads, bibframe.SubjectQuad(res.WorkID, subj.URI))
		term := rdf.NewIRI(subj.URI)
		langs := make([]string, 0, len(subj.Labels))
		for lang := range subj.Labels {
			langs = append(langs, lang)
		}
		sort.Strings(langs)
		for _, lang := range langs {
			quads = append(quads, rdf.Quad{
				S: term,
				P: rdf.NewIRI("http://www.w3.org/2004/02/skos/core#prefLabel"),
				O: rdf.NewLiteral(subj.Labels[lang], lang, ""),
			})
		}
		for _, parent := range subj.Broader {
			quads = append(quads, rdf.Quad{
				S: term,
				P: rdf.NewIRI("http://www.w3.org/2004/02/skos/core#broader"),
				O: rdf.NewIRI(parent),
			})
		}
	}
	return quads
}

// replaceGrainGraph swaps one grain's named graph under a conditional write,
// retrying from fresh on conflict.
func replaceGrainGraph(ctx context.Context, st blob.Store, grainPath string, graph rdf.Term, quads []rdf.Quad) error {
	for range 6 {
		grain, etag, err := st.Get(ctx, grainPath)
		if err != nil {
			return err
		}
		updated, err := bibframe.ReplaceGraph(grain, graph, quads)
		if err != nil {
			return err
		}
		_, err = st.Put(ctx, grainPath, updated, blob.PutOptions{IfMatch: etag, ContentType: "application/n-quads"})
		if err == nil {
			return nil
		}
		if err != blob.ErrPreconditionFailed {
			return err
		}
	}
	return fmt.Errorf("ingest: enrichment write kept conflicting")
}

// ScanSummaries walks the grain tree and extracts a WorkSummary per Work,
// plus each Work's grain path.
func ScanSummaries(ctx context.Context, st blob.Store, prefix string) ([]WorkSummary, map[string]string, error) {
	var summaries []WorkSummary
	paths := map[string]string{}
	for entry, err := range st.List(ctx, prefix) {
		if err != nil {
			return nil, nil, err
		}
		base := path.Base(entry.Path)
		if !strings.HasSuffix(base, ".nq") || base == "catalog.nq" {
			continue
		}
		grain, _, err := st.Get(ctx, entry.Path)
		if err != nil {
			return nil, nil, err
		}
		grainSummaries, err := summarizeGrain(grain)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", entry.Path, err)
		}
		for _, s := range grainSummaries {
			paths[s.WorkID] = entry.Path
			summaries = append(summaries, s)
		}
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].WorkID < summaries[j].WorkID })
	return summaries, paths, nil
}

// summarizeGrain extracts the WorkSummaries a grain carries (post-merge
// grains can hold several Works).
func summarizeGrain(grain []byte) ([]WorkSummary, error) {
	const (
		bfNS      = "http://id.loc.gov/ontologies/bibframe/"
		rdfsLabel = "http://www.w3.org/2000/01/rdf-schema#label"
	)
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return nil, err
	}
	// One merged view over all graphs; enrichers see feed + editorial data.
	merged := &rdf.Graph{}
	for _, gt := range ds.Graphs() {
		g := ds.Graph(gt)
		for _, tr := range g.Triples {
			merged.Add(tr.S, tr.P, tr.O)
		}
	}
	var out []WorkSummary
	for _, work := range merged.SubjectsOfType(bfNS + "Work") {
		id := strings.TrimSuffix(strings.TrimPrefix(work.Value, "#"), "Work")
		if !strings.HasPrefix(work.Value, "#") || id == "" {
			continue
		}
		s := WorkSummary{WorkID: id}
		if title, ok := merged.Object(work, bfNS+"title"); ok {
			if main, ok := merged.Literal(title, bfNS+"mainTitle"); ok {
				s.Title = main
			}
		}
		for _, contrib := range merged.Objects(work, bfNS+"contribution") {
			if agent, ok := merged.Object(contrib, bfNS+"agent"); ok {
				if name, ok := merged.Literal(agent, rdfsLabel); ok {
					s.Contributors = append(s.Contributors, name)
				}
			}
		}
		for _, subj := range merged.Objects(work, bfNS+"subject") {
			if subj.IsBlank() {
				if label, ok := merged.Literal(subj, rdfsLabel); ok {
					s.Tags = append(s.Tags, label)
				}
			}
		}
		for _, tag := range merged.Objects(work, bibframe.PredTag) {
			if tag.IsLiteral() {
				s.Tags = append(s.Tags, tag.Value)
			}
		}
		for _, inst := range merged.Objects(work, bfNS+"hasInstance") {
			for _, ident := range merged.Objects(inst, bfNS+"identifiedBy") {
				if merged.HasType(ident, bfNS+"Isbn") {
					if v, ok := merged.Literal(ident, "http://www.w3.org/1999/02/22-rdf-syntax-ns#value"); ok {
						s.ISBNs = append(s.ISBNs, v)
					}
				}
			}
		}
		sort.Strings(s.Tags)
		sort.Strings(s.ISBNs)
		out = append(out, s)
	}
	return out, nil
}
