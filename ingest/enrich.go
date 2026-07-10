package ingest

import (
	"context"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"

	"github.com/freeeve/libcodex/rdf"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
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
	// Subjects are the Work's controlled subject IRIs (bf:subject with an
	// IRI object, any graph) -- what authority merges rewrite (tasks/046).
	Subjects []string
	// Series and Languages are the similarity scorer's two strongest signals
	// (tasks/284): two books in one series are related whatever else they
	// share, and a reader of one language is rarely served a book in another.
	// Series is transcribed per Instance (bf:seriesStatement, 490$a) and
	// hoisted to the Work here; Languages are bf:language local names ("en").
	Series    []string `json:",omitempty"`
	Languages []string `json:",omitempty"`
	// Visibility and holdings signals for the admin works list (tasks/078):
	// the editor deliberately shows everything, so each row says what the
	// public projection would do with it.
	Suppressed bool   `json:",omitempty"`
	Tombstoned bool   `json:",omitempty"`
	Withdrawn  string `json:",omitempty"` // date the feed reconciliation flagged it
	Kept       bool   `json:",omitempty"` // curator keeps it despite withdrawal
	// Items counts physical holdings across the Work's instances;
	// HasAvailability reports a live-availability identifier (a digital
	// holding as long as the Work is not withdrawn).
	Items           int  `json:",omitempty"`
	HasAvailability bool `json:",omitempty"`
	// Extras carries the Work's lcat:extra/* literals -- deployment-defined
	// key/value metadata the feed graph attaches (bibframe.addWorkExtras),
	// e.g. sources: "loc, mombian". The admin works view facets provenance
	// on configured keys (tasks/171); multi-valued keys are comma-joined by
	// convention and split at facet time.
	Extras map[string]string `json:",omitempty"`
}

// Matches reports whether the summary matches a lowercased search query --
// substring over title, id, contributors, tags, and ISBNs. One matcher
// serves the works listing and batch search selections (tasks/047), so a
// saved query means the same thing everywhere.
func (s WorkSummary) Matches(q string) bool {
	if strings.Contains(strings.ToLower(s.Title), q) || strings.Contains(strings.ToLower(s.WorkID), q) {
		return true
	}
	for _, c := range s.Contributors {
		if strings.Contains(strings.ToLower(c), q) {
			return true
		}
	}
	for _, tag := range s.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return true
		}
	}
	for _, isbn := range s.ISBNs {
		if strings.Contains(isbn, q) {
			return true
		}
	}
	return false
}

// Enrichment is one Work's enrichment result: controlled subjects to assert.
type Enrichment struct {
	WorkID   string
	Subjects []AuthoritySubject
	// Terms carries standalone term descriptions -- typically the
	// skos:broader ancestor chains of Subjects -- asserted into the
	// enrichment graph as labels + hierarchy only, with no link to the Work
	// (tasks/178). They keep ancestor nodes labeled in projections that roll
	// subtree data up the hierarchy even when no work carries the ancestor
	// directly.
	Terms []AuthoritySubject
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
	// Collect every batch's results before writing: post-merge grains hold
	// several Works sharing one path, and replacing the graph once per Work
	// would wipe the sibling Work's statements (tasks/102) -- so group by
	// grain and write each grain exactly once.
	byGrain := map[string][]Enrichment{}
	for start := 0; start < len(summaries); start += enrichBatchSize {
		end := min(start+enrichBatchSize, len(summaries))
		results, err := e.Enrich(ctx, summaries[start:end])
		if err != nil {
			return 0, fmt.Errorf("enrich %s: %w", e.Name(), err)
		}
		for _, res := range results {
			grainPath, ok := paths[res.WorkID]
			if !ok {
				continue
			}
			byGrain[grainPath] = append(byGrain[grainPath], res)
		}
	}
	grainPaths := make([]string, 0, len(byGrain))
	for p := range byGrain {
		grainPaths = append(grainPaths, p)
	}
	sort.Strings(grainPaths)
	enriched := 0
	for _, grainPath := range grainPaths {
		if err := replaceGrainEnrichment(ctx, st, grainPath, graph, byGrain[grainPath]); err != nil {
			return enriched, fmt.Errorf("%s: %w", grainPath, err)
		}
		enriched += len(byGrain[grainPath])
	}
	return enriched, nil
}

// enrichmentQuads renders one enrichment as self-contained statements: the
// subject links plus each term's labels and hierarchy, then the standalone
// Terms descriptions (ancestor-chain metadata with no work link, tasks/178),
// all destined for the enricher's own graph.
func enrichmentQuads(res Enrichment) []rdf.Quad {
	var quads []rdf.Quad
	for _, subj := range res.Subjects {
		quads = append(quads, bibframe.SubjectQuad(res.WorkID, subj.URI))
		quads = append(quads, termQuads(subj)...)
	}
	for _, term := range res.Terms {
		quads = append(quads, termQuads(term)...)
	}
	return quads
}

// termQuads renders one term's description: skos:prefLabel per language
// (sorted for determinism) and skos:broader per parent.
func termQuads(subj AuthoritySubject) []rdf.Quad {
	var quads []rdf.Quad
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
	return quads
}

// replaceGrainEnrichment rewrites one grain's enrichment graph under a
// conditional write, retrying from fresh on conflict. The graph's new
// contents are the fresh statements for the Works in results, plus the
// preserved statements of any co-grained Work the enricher did not return --
// those must stay untouched per RunEnrich's contract (tasks/102).
func replaceGrainEnrichment(ctx context.Context, st blob.Store, grainPath string, graph rdf.Term, results []Enrichment) error {
	resolved := map[string]bool{}
	var fresh []rdf.Quad
	for _, res := range results {
		resolved[res.WorkID] = true
		fresh = append(fresh, enrichmentQuads(res)...)
	}
	for range 6 {
		grain, etag, err := st.Get(ctx, grainPath)
		if err != nil {
			return err
		}
		quads, err := withPreservedEnrichment(grain, graph, resolved, fresh)
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

// withPreservedEnrichment extends fresh with the grain's existing
// enrichment-graph statements that belong to Works absent from resolved: their
// subject links, and the term descriptions those links still reference --
// except terms the fresh statements re-describe, where fresh wins.
func withPreservedEnrichment(grain []byte, graph rdf.Term, resolved map[string]bool, fresh []rdf.Quad) ([]rdf.Quad, error) {
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return nil, err
	}
	freshTerms := map[string]bool{}
	for _, q := range fresh {
		if grainWorkID(q.S) == "" {
			freshTerms[q.S.Value] = true
		}
	}
	out := fresh
	referenced := map[string]bool{}
	termQuads := map[string][]rdf.Quad{}
	for _, q := range ds.Quads {
		if q.G != graph {
			continue
		}
		if wid := grainWorkID(q.S); wid != "" {
			if !resolved[wid] {
				out = append(out, q)
				if q.O.IsIRI() {
					referenced[q.O.Value] = true
				}
			}
			continue
		}
		termQuads[q.S.Value] = append(termQuads[q.S.Value], q)
	}
	for term, quads := range termQuads {
		if referenced[term] && !freshTerms[term] {
			out = append(out, quads...)
		}
	}
	return out, nil
}

// grainWorkID extracts the Work id from a grain-local Work IRI ("#<id>Work"),
// or "" when the term is not one.
func grainWorkID(t rdf.Term) string {
	if !t.IsIRI() || !strings.HasPrefix(t.Value, "#") || !strings.HasSuffix(t.Value, "Work") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(t.Value, "#"), "Work")
}

// availabilitySources are the bf:source schemes a runtime availability
// adapter can resolve -- the digital-holding signal (tasks/078); mirrors the
// projector's list.
var availabilitySources = map[string]bool{"overdrive-reserve": true}

// SummarySource yields the corpus's WorkSummaries plus each Work's grain
// path without a fresh corpus walk -- the seam a maintained index (the
// backend's workindex, tasks/106/109) plugs into where workers would
// otherwise each run their own ScanSummaries. Both return values are shared,
// read-only views.
type SummarySource interface {
	SummariesWithPaths(ctx context.Context) ([]WorkSummary, map[string]string, error)
}

// SummariesOf reads summaries from src when one is wired, falling back to a
// fresh ScanSummaries walk of prefix.
func SummariesOf(ctx context.Context, src SummarySource, st blob.Store, prefix string) ([]WorkSummary, map[string]string, error) {
	if src != nil {
		return src.SummariesWithPaths(ctx)
	}
	return ScanSummaries(ctx, st, prefix)
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
		grainSummaries, err := SummarizeGrain(grain)
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

// SummarizeGrain extracts the WorkSummaries a grain carries (post-merge
// grains can hold several Works). Exported for callers that already hold the
// grain bytes, like the on-save authority auto-linker (tasks/046).
func SummarizeGrain(grain []byte) ([]WorkSummary, error) {
	ds, err := rdf.ParseNQuads(grain)
	if err != nil {
		return nil, err
	}
	return SummarizeDataset(ds), nil
}

// sortedUnique sorts and de-duplicates. Several Instances of one Work routinely
// transcribe the same series statement and carry the same language, and a
// similarity scorer that counted them twice would rank a Work with three
// printings above a Work with one.
func sortedUnique(vs []string) []string {
	if len(vs) < 2 {
		return vs
	}
	sort.Strings(vs)
	return slices.Compact(vs)
}

// SummarizeDataset is SummarizeGrain for callers that already hold the parsed
// dataset (the work index scans identity, summaries, and barcodes off one
// parse).
func SummarizeDataset(ds *rdf.Dataset) []WorkSummary {
	const (
		bfNS      = "http://id.loc.gov/ontologies/bibframe/"
		rdfsLabel = "http://www.w3.org/2000/01/rdf-schema#label"
	)
	// One merged view over all graphs; enrichers see feed + editorial data.
	// Built in one exactly-sized pass straight off the quads, in the same
	// graph-grouped order Dataset.Graph would yield: materializing each
	// graph and re-Adding tripled the copies and dominated the per-grain
	// allocation profile at workindex boot/refresh scale (tasks/121).
	merged := &rdf.Graph{Triples: make([]rdf.Triple, 0, len(ds.Quads))}
	for _, gt := range ds.Graphs() {
		for i := range ds.Quads {
			if q := &ds.Quads[i]; q.G == gt {
				merged.Triples = append(merged.Triples, rdf.Triple{S: q.S, P: q.P, O: q.O})
			}
		}
	}
	// lcat:extra/* literals by subject, one pass: deployment-defined Work
	// metadata the feed graph carries (tasks/171).
	extras := map[string]map[string]string{}
	for _, tr := range merged.Triples {
		key, ok := strings.CutPrefix(tr.P.Value, bibframe.ExtraPred)
		if !ok || key == "" || !tr.O.IsLiteral() {
			continue
		}
		m := extras[tr.S.Value]
		if m == nil {
			m = map[string]string{}
			extras[tr.S.Value] = m
		}
		m[key] = tr.O.Value
	}
	var out []WorkSummary
	for _, work := range merged.SubjectsOfType(bfNS + "Work") {
		id := strings.TrimSuffix(strings.TrimPrefix(work.Value, "#"), "Work")
		if !strings.HasPrefix(work.Value, "#") || id == "" {
			continue
		}
		s := WorkSummary{WorkID: id, Extras: extras[work.Value]}
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
			// A grain-local node (blank or fragment IRI like an editor
			// skolem) is an uncontrolled heading, not a controlled term
			// (tasks/218).
			if subj.IsBlank() || (subj.IsIRI() && bibframe.GrainLocalIRI(subj.Value)) {
				if label, ok := merged.Literal(subj, rdfsLabel); ok {
					s.Tags = append(s.Tags, label)
				}
			} else if subj.IsIRI() {
				s.Subjects = append(s.Subjects, subj.Value)
			}
		}
		for _, tag := range merged.Objects(work, bibframe.PredTag) {
			if tag.IsLiteral() {
				s.Tags = append(s.Tags, tag.Value)
			}
		}
		for _, lang := range merged.Objects(work, bfNS+"language") {
			if code := rdf.LocalName(lang.Value); code != "" {
				s.Languages = append(s.Languages, code)
			}
		}
		for _, inst := range merged.Objects(work, bfNS+"hasInstance") {
			for _, ident := range merged.Objects(inst, bfNS+"identifiedBy") {
				if merged.HasType(ident, bfNS+"Isbn") {
					if v, ok := merged.Literal(ident, "http://www.w3.org/1999/02/22-rdf-syntax-ns#value"); ok {
						s.ISBNs = append(s.ISBNs, v)
					}
					continue
				}
				if src, ok := merged.Object(ident, bfNS+"source"); ok {
					if label, ok := merged.Literal(src, rdfsLabel); ok && availabilitySources[label] {
						s.HasAvailability = true
					}
				}
			}
			// The series statement is transcribed on the Instance, but similarity
			// is a Work-level question: a reader wants the next book, not the next
			// printing (tasks/284).
			for _, ser := range merged.Objects(inst, bfNS+"seriesStatement") {
				if ser.IsLiteral() && ser.Value != "" {
					s.Series = append(s.Series, ser.Value)
				}
			}
			s.Items += len(merged.Objects(inst, bfNS+"hasItem"))
		}
		// Visibility + reconciliation stance (tasks/078); statements are
		// editorial, so the merged view carries them.
		s.Tombstoned = len(merged.Objects(work, bibframe.PredTombstoned)) > 0
		if v, ok := merged.Literal(work, bibframe.PredSuppressed); ok {
			s.Suppressed = v == "true"
		}
		if v, ok := merged.Literal(work, bibframe.PredWithdrawn); ok {
			s.Withdrawn = v
		}
		if v, ok := merged.Literal(work, bibframe.PredFeedKept); ok {
			s.Kept = v == "true"
		}
		sort.Strings(s.Tags)
		sort.Strings(s.Subjects)
		sort.Strings(s.ISBNs)
		s.Series = sortedUnique(s.Series)
		s.Languages = sortedUnique(s.Languages)
		out = append(out, s)
	}
	return out
}
