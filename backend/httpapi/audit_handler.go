package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/freeeve/libcat/diversity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/project"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/backend/workindex"
)

// auditResponse is the diversity report plus what it was computed over, matching
// the `lcat audit` JSON shape so a saved report reads the same either way. The
// creators block aggregates the cached wikidata claims when any exist.
type auditResponse struct {
	Input string `json:"input"`
	Scope string `json:"scope,omitempty"`
	diversity.Report
	// LabelLanguages is the configured subject-label language set, in column
	// order -- the keys each category's LabelLangWorks map is reported against,
	// so a consumer renders one column per language even where a category has
	// zero coverage in it. These count subject-heading reachability, not the
	// book's own language.
	LabelLanguages []string      `json:"labelLanguages,omitempty"`
	Creators       *creatorAudit `json:"creators,omitempty"`
	// ResourceLanguages is the distribution of the scope's works by the language
	// the works are IN (bf:language), distinct from the subject-label columns
	// (which are heading reachability). nil when no scoped work declares a
	// language, so "not carried" reads differently from "0".
	ResourceLanguages *resourceLanguageAudit `json:"resourceLanguages,omitempty"`
	// Simulation is the read-only "if we accepted the queue" projection,
	// present only when ?simulate=queue was asked. The top-level report stays
	// the current corpus so the screen can diff current vs projected.
	Simulation *auditSimulation `json:"simulation,omitempty"`
}

// auditSimulation reports what the diversity audit WOULD look like if every
// pending ADD suggestion matching the queue filter were accepted -- a read-only
// union of each work's current subjects with its pending suggested terms, run
// through the same crosswalk. It touches no grains and no queue statuses.
type auditSimulation struct {
	// Filter echoes the queue scope the projection honoured (the review
	// screen's own filters: confidence floor, provenance, scheme).
	Filter string `json:"filter"`
	// Applied is how many pending ADD suggestions were unioned in; Works is the
	// distinct works they touched.
	Applied int `json:"applied"`
	Works   int `json:"works"`
	// Projected is the audit over the current subjects PLUS those suggestions.
	Projected diversity.Report `json:"projected"`
}

// creatorAudit is the aggregate creator-demographics report: match rate first,
// then per-property value distributions over DISTINCT resolved creators. It
// never names a person -- distributions and counts only.
type creatorAudit struct {
	// TotalWorks mirrors the content report's denominator; MatchedWorks is
	// how many carry at least one resolved creator identity.
	TotalWorks   int     `json:"totalWorks"`
	MatchedWorks int     `json:"matchedWorks"`
	MatchRate    float64 `json:"matchRate"`
	// ResolvedCreators is the number of distinct creator entities.
	ResolvedCreators int               `json:"resolvedCreators"`
	Properties       []creatorProperty `json:"properties"`
}

// creatorProperty is one demographic property's distribution. Unknown is the
// resolved creators with NO stated value for this property -- reported
// alongside every distribution because it is usually the honest majority.
type creatorProperty struct {
	Property string         `json:"property"`
	Label    string         `json:"label"`
	Known    int            `json:"known"`
	Unknown  int            `json:"unknown"`
	Values   []creatorValue `json:"values,omitempty"`
}

// creatorValue is one claim value and how many distinct creators carry it.
type creatorValue struct {
	Label    string `json:"label"`
	QID      string `json:"qid"`
	Creators int    `json:"creators"`
}

// creatorPropLabels names the audited properties for display.
var creatorPropLabels = []struct{ id, label string }{
	{"P21", "Sex or gender"},
	{"P27", "Country of citizenship"},
	{"P91", "Sexual orientation"},
	{"P172", "Ethnic group"},
}

// aggregateCreators folds the summaries' cached creator claims into the
// aggregate report; nil when the corpus carries no creator data at all (the
// source is opt-in, and "not enabled" must read differently from "0% match").
func aggregateCreators(sums []ingest.WorkSummary, include func(*ingest.WorkSummary) bool) *creatorAudit {
	ca := &creatorAudit{}
	creators := map[string]map[string][]string{} // QID -> property -> value QIDs
	valueLabels := map[string]string{}
	for i := range sums {
		s := &sums[i]
		if !include(s) {
			continue
		}
		ca.TotalWorks++
		if len(s.Creators) == 0 {
			continue
		}
		ca.MatchedWorks++
		for _, c := range s.Creators {
			props := creators[c.QID]
			if props == nil {
				props = map[string][]string{}
				creators[c.QID] = props
			}
			for _, cl := range c.Claims {
				if !slices.Contains(props[cl.Property], cl.ValueQID) {
					props[cl.Property] = append(props[cl.Property], cl.ValueQID)
				}
				if cl.ValueLabel != "" {
					valueLabels[cl.ValueQID] = cl.ValueLabel
				}
			}
		}
	}
	if len(creators) == 0 {
		return nil
	}
	ca.ResolvedCreators = len(creators)
	if ca.TotalWorks > 0 {
		ca.MatchRate = float64(ca.MatchedWorks) / float64(ca.TotalWorks)
	}
	for _, p := range creatorPropLabels {
		cp := creatorProperty{Property: p.id, Label: p.label}
		counts := map[string]int{}
		for _, props := range creators {
			vals := props[p.id]
			if len(vals) == 0 {
				continue
			}
			cp.Known++
			for _, v := range vals {
				counts[v]++
			}
		}
		cp.Unknown = ca.ResolvedCreators - cp.Known
		for qid, n := range counts {
			label := valueLabels[qid]
			if label == "" {
				label = qid
			}
			cp.Values = append(cp.Values, creatorValue{Label: label, QID: qid, Creators: n})
		}
		sort.Slice(cp.Values, func(i, j int) bool {
			if cp.Values[i].Creators != cp.Values[j].Creators {
				return cp.Values[i].Creators > cp.Values[j].Creators
			}
			return cp.Values[i].Label < cp.Values[j].Label
		})
		ca.Properties = append(ca.Properties, cp)
	}
	return ca
}

// resourceLanguageAudit is the scope's distribution of works by the language
// the works are IN (bf:language / Work.Languages), the real book language --
// deliberately separate from the subject-label reachability columns. Following
// BIBFRAME practice, language is a Work attribute (a translation is its own
// Work), so this counts at the Work level.
//
// The buckets are EXCLUSIVE: a work declaring one language counts under that
// language; a work declaring two or more counts once under Multilingual, never
// under each. This is deliberate and honest -- a source that stamps the full
// set of languages a title is available in onto every record (rather than the
// one language of this copy) would otherwise let an English book with a Spanish
// edition inflate the Spanish count. Languages + Multilingual therefore sum to
// WithLanguage, which is itself the honest denominator (declaring no language is
// common and must not read as a language).
type resourceLanguageAudit struct {
	// TotalWorks is every scoped work; WithLanguage is those declaring at least
	// one bf:language.
	TotalWorks   int `json:"totalWorks"`
	WithLanguage int `json:"withLanguage"`
	// Multilingual is works declaring two or more distinct languages, counted
	// once here instead of under each language (see the type doc).
	Multilingual int `json:"multilingual"`
	// Languages is the single-language work count per language, most works first.
	// Multi-language works are excluded here (they are in Multilingual), so
	// summing Languages and Multilingual gives WithLanguage.
	Languages []resourceLanguageCount `json:"languages"`
}

// resourceLanguageCount is one language and how many scoped single-language
// works are in it. Code is the ISO 639-2/B code as stored (bf:language local
// name), e.g. "spa".
type resourceLanguageCount struct {
	Code  string `json:"code"`
	Works int    `json:"works"`
}

// aggregateResourceLanguages folds the scoped summaries' Work.Languages into the
// resource-language distribution; nil when no scoped work declares a language,
// so "no language data carried" reads differently from "0 in this language". A
// work with one distinct language counts under it; a work with two or more
// counts once as Multilingual (see resourceLanguageAudit).
func aggregateResourceLanguages(sums []ingest.WorkSummary, include func(*ingest.WorkSummary) bool) *resourceLanguageAudit {
	ra := &resourceLanguageAudit{}
	counts := map[string]int{}
	any := false
	for i := range sums {
		s := &sums[i]
		if !include(s) {
			continue
		}
		ra.TotalWorks++
		seen := map[string]bool{}
		for _, code := range s.Languages {
			if code != "" {
				seen[code] = true
			}
		}
		switch len(seen) {
		case 0:
			continue
		case 1:
			ra.WithLanguage++
			any = true
			for code := range seen {
				counts[code]++
			}
		default:
			ra.WithLanguage++
			ra.Multilingual++
			any = true
		}
	}
	if !any {
		return nil
	}
	for code, n := range counts {
		ra.Languages = append(ra.Languages, resourceLanguageCount{Code: code, Works: n})
	}
	sort.Slice(ra.Languages, func(i, j int) bool {
		if ra.Languages[i].Works != ra.Languages[j].Works {
			return ra.Languages[i].Works > ra.Languages[j].Works
		}
		return ra.Languages[i].Code < ra.Languages[j].Code
	})
	return ra
}

// auditCache memoizes computed reports against the work index generation: the
// audit is a pure function of (corpus, crosswalk, filters), the generation is
// the corpus's change counter, and the crosswalk joins the key through its
// override-content hash (crosswalkSource), so saving a new override never
// serves a stale report. Entries key on the NORMALIZED filter set so term
// order does not fork the cache; a generation change drops everything, and
// the map is capped because filters are caller-chosen text.
type auditCache struct {
	mu      sync.Mutex
	gen     uint64
	entries map[string]auditResponse
}

// auditCacheCap bounds distinct filter sets kept per generation; overflow
// clears wholesale (recomputing is milliseconds; bookkeeping an LRU is not
// worth it here).
const auditCacheCap = 64

// get returns the cached response for the key at the given generation.
func (c *auditCache) get(gen uint64, key string) (auditResponse, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen || c.entries == nil {
		return auditResponse{}, false
	}
	r, ok := c.entries[key]
	return r, ok
}

// put stores a computed response, invalidating older generations.
func (c *auditCache) put(gen uint64, key string, r auditResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen != gen || c.entries == nil || len(c.entries) >= auditCacheCap {
		c.gen = gen
		c.entries = map[string]auditResponse{}
	}
	c.entries[key] = r
}

// cacheKey is the normalized filter set: sorted terms, so "a=1 b=2" and
// "b=2 a=1" hit the same entry.
func (f auditFilterSet) cacheKey() string {
	terms := make([]string, 0, len(f))
	for _, p := range f {
		terms = append(terms, p[0]+"="+p[1])
	}
	sort.Strings(terms)
	return strings.Join(terms, "\x00")
}

// registerAudit serves the content-diversity audit over the live work index
// : the same coverage-first report `lcat audit` computes, but against
// the cataloging corpus the editor sees -- suppressed works included (they are
// held, just not published), tombstoned works excluded (they are retired).
// Aggregation is O(corpus) string matching, memoized against the index
// generation (auditCache) so a page re-view or a scope toggle back returns
// instantly; the index owns freshness.
//
// Query: filter=key=value (repeatable, ANDed; comma-joined extras match per
// element) and source=<name>, both matching the summaries' Extras -- the same
// semantics as `lcat audit --filter/--source`.
// registerAudit returns its compute path so the snapshot recorder reuses the
// same filters/cache/aggregation (registerAuditSnapshots).
func registerAudit(mux *http.ServeMux, ix *workindex.Index, vix *vocab.Index, auditLangs []string, svc *suggest.Service, verifier auth.TokenVerifier, cws *crosswalkSource) func(*http.Request) (auditResponse, auditFilterSet, int, error) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	cache := &auditCache{}

	compute := func(r *http.Request) (auditResponse, auditFilterSet, int, error) {
		filters, err := auditFilters(r)
		if err != nil {
			return auditResponse{}, nil, http.StatusBadRequest, err
		}
		simQ, wantSim, err := simulateQuery(r)
		if err != nil {
			return auditResponse{}, nil, http.StatusBadRequest, err
		}
		if wantSim && svc == nil {
			return auditResponse{}, nil, http.StatusServiceUnavailable, errors.New("queue simulation unavailable")
		}
		sums, gen, err := ix.SummariesWithGeneration(r.Context())
		if err != nil {
			return auditResponse{}, nil, http.StatusInternalServerError, errScanFailed
		}
		cw, cwHash, _, err := cws.effective(r)
		if err != nil {
			return auditResponse{}, nil, http.StatusInternalServerError, errScanFailed
		}
		// The plain audit caches on (crosswalk, filters, corpus generation).
		// A queue simulation also depends on queue state, which the generation
		// counter does not track, so it is computed fresh every time -- it is a
		// read-only, explicitly-requested projection.
		key := cwHash + "\x00" + filters.cacheKey()
		if !wantSim {
			if resp, ok := cache.get(gen, key); ok {
				return resp, filters, http.StatusOK, nil
			}
		}
		include := func(s *ingest.WorkSummary) bool {
			return includeInAudit(s, filters)
		}
		a := diversity.NewAuditor(cw)
		var sim *auditSimulation
		if wantSim {
			suggested, applied, works, serr := queuedRefsByWork(r.Context(), svc, simQ, vix, auditLangs)
			if serr != nil {
				return auditResponse{}, nil, http.StatusInternalServerError, errScanFailed
			}
			proj := diversity.NewAuditor(cw)
			for i := range sums {
				s := &sums[i]
				if !include(s) {
					continue
				}
				refs := summaryRefs(s, vix, auditLangs)
				w := workWeight(s)
				a.AddWeighted(refs, w)
				proj.AddWeighted(append(refs, suggested[s.WorkID]...), w)
			}
			sim = &auditSimulation{
				Filter:    describeSimQuery(simQ),
				Applied:   applied,
				Works:     works,
				Projected: proj.Report(),
			}
		} else {
			for i := range sums {
				s := &sums[i]
				if !include(s) {
					continue
				}
				a.AddWeighted(summaryRefs(s, vix, auditLangs), workWeight(s))
			}
		}
		resp := auditResponse{
			Input:             "work index (cataloging corpus: suppressed included, tombstoned excluded)",
			Scope:             filters.String(),
			Report:            a.Report(),
			LabelLanguages:    auditLangs,
			Creators:          aggregateCreators(sums, include),
			ResourceLanguages: aggregateResourceLanguages(sums, include),
			Simulation:        sim,
		}
		if !wantSim {
			cache.put(gen, key, resp)
		}
		return resp, filters, http.StatusOK, nil
	}

	mux.Handle("GET /v1/audit/diversity", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, _, status, err := compute(r)
		if err != nil {
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})))
	return compute
}

// errScanFailed keeps the surfaced message stable ("scan failed") without
// leaking the index error's internals.
var errScanFailed = errors.New("scan failed")

// auditFilters parses ?filter=k=v (repeatable) and ?source=<name> into the same
// ANDed filter set the CLI uses.
func auditFilters(r *http.Request) (auditFilterSet, error) {
	var out auditFilterSet
	q := r.URL.Query()
	for _, raw := range q["filter"] {
		k, v, ok := strings.Cut(raw, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("filter wants key=value, got %q", raw)
		}
		out = append(out, [2]string{k, v})
	}
	if s := q.Get("source"); s != "" {
		out = append(out, [2]string{"sources", s})
	}
	return out, nil
}

// auditFilterSet is the endpoint's ANDed extras filters.
type auditFilterSet [][2]string

// String renders the active filters for the response's scope field.
func (f auditFilterSet) String() string {
	parts := make([]string, 0, len(f))
	for _, p := range f {
		parts = append(parts, p[0]+"="+p[1])
	}
	return strings.Join(parts, " AND ")
}

// match reports whether a summary's extras satisfy every filter; a comma-joined
// extra (the sources convention) matches on any element. The semantic lives in
// ingest.MatchExtras so async enrichment jobs scope identically.
func (f auditFilterSet) match(extra map[string]string) bool {
	return ingest.MatchExtras(f, extra)
}

// summaryRefs turns a work summary's aboutness signal into audit refs: controlled
// subject IRIs (scheme from the URI namespace), their heading labels, and the
// uncontrolled tags -- the same three dimensions the CLI's json and graph inputs
// feed, so all three surfaces measure the same thing.
func summaryRefs(s *ingest.WorkSummary, vix *vocab.Index, langs []string) []diversity.SubjectRef {
	refs := make([]diversity.SubjectRef, 0, len(s.Subjects)+len(s.Headings)+len(s.Tags))
	for _, uri := range s.Subjects {
		refs = append(refs, diversity.SubjectRef{URI: uri, Scheme: project.SchemeForURI(uri), Langs: subjectLabelLangs(vix, uri, langs)})
	}
	for _, h := range s.Headings {
		refs = append(refs, diversity.SubjectRef{Labels: []string{h}})
	}
	for _, t := range s.Tags {
		refs = append(refs, diversity.SubjectRef{Labels: []string{t}})
	}
	return refs
}

// workWeight is the per-work weight the audit tallies alongside title counts:
// the copies the library holds, from the ownedCopies extra a holdings provider
// (OverDrive) stamps on the work. Absent or unparseable reads as 0, so a corpus
// with no holdings data simply reports no weight. It lets a category be read by
// collection depth (copies held) rather than title count.
func workWeight(s *ingest.WorkSummary) int {
	v := s.Extras["ownedCopies"]
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// simulateQuery reads the queue-simulation params off the audit request. It
// returns wantSim=false when ?simulate is absent; only "queue" is a valid
// value. The scope mirrors the review screen's queue filters -- confidence
// floor (the interesting "everything above 0.85" question), provenance, and
// scheme -- always over PENDING ADD suggestions, since only an accepted ADD
// broadens what a work is about.
func simulateQuery(r *http.Request) (suggest.QueueQuery, bool, error) {
	mode := r.URL.Query().Get("simulate")
	if mode == "" {
		return suggest.QueueQuery{}, false, nil
	}
	if mode != "queue" {
		return suggest.QueueQuery{}, false, fmt.Errorf("simulate wants 'queue', got %q", mode)
	}
	q := suggest.QueueQuery{Status: suggest.StatusPending, Type: suggest.TypeAdd}
	if v := r.URL.Query().Get("minConfidence"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < 0 || f > 1 {
			return suggest.QueueQuery{}, false, fmt.Errorf("minConfidence wants a number in [0,1], got %q", v)
		}
		q.MinConfidence = f
	}
	if v := r.URL.Query().Get("provenance"); v != "" {
		q.Provenance = suggest.Provenance(v)
	}
	if v := r.URL.Query().Get("scheme"); v != "" {
		q.Scheme = v
	}
	return q, true, nil
}

// queuedRefsByWork walks every pending ADD suggestion matching q and groups
// them into per-work audit refs, so the projection can union them with each
// work's current subjects. It also returns how many suggestions were folded in
// and how many distinct works they touched. Read-only: it never mutates a
// suggestion or a grain.
func queuedRefsByWork(ctx context.Context, svc *suggest.Service, q suggest.QueueQuery, vix *vocab.Index, langs []string) (map[string][]diversity.SubjectRef, int, int, error) {
	byWork := map[string][]diversity.SubjectRef{}
	applied := 0
	err := svc.EachQueued(ctx, q, func(sg suggest.Suggestion) error {
		byWork[sg.WorkID] = append(byWork[sg.WorkID], termToSubjectRef(sg.Term, vix, langs))
		applied++
		return nil
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return byWork, applied, len(byWork), nil
}

// termToSubjectRef adapts a suggestion's TermRef (scheme,id,label) to the audit's
// SubjectRef (uri,labels,scheme): the term id is the authority IRI for a
// controlled term, the label feeds keyword matching, and the scheme rides along.
// A work gains coverage from an accepted term, which is the projection's point.
func termToSubjectRef(t vocab.TermRef, vix *vocab.Index, langs []string) diversity.SubjectRef {
	ref := diversity.SubjectRef{URI: t.ID, Scheme: t.Scheme, Langs: subjectLabelLangs(vix, t.ID, langs)}
	if strings.TrimSpace(t.Label) != "" {
		ref.Labels = []string{t.Label}
	}
	return ref
}

// subjectLabelLangs returns which of the configured audit languages the
// controlled term at uri carries a label in -- the per-language subject-heading
// reachability signal. It is nil- and miss-safe: no vocab index, an empty uri,
// or an unresolved term all yield no languages, so an uncontrolled heading never
// counts toward any language column. Order follows the configured list.
func subjectLabelLangs(vix *vocab.Index, uri string, langs []string) []string {
	if vix == nil || uri == "" || len(langs) == 0 {
		return nil
	}
	t, ok := vix.Resolve(uri)
	if !ok {
		return nil
	}
	var out []string
	for _, lang := range langs {
		if t.HasLabelLang(lang) {
			out = append(out, lang)
		}
	}
	return out
}

// describeSimQuery renders the applied queue scope for the response's Filter
// field: always PENDING ADD, plus whichever optional filters were set.
func describeSimQuery(q suggest.QueueQuery) string {
	parts := []string{"status=PENDING", "type=ADD"}
	if q.MinConfidence > 0 {
		parts = append(parts, fmt.Sprintf("minConfidence>=%g", q.MinConfidence))
	}
	if q.Provenance != "" {
		parts = append(parts, "provenance="+string(q.Provenance))
	}
	if q.Scheme != "" {
		parts = append(parts, "scheme="+q.Scheme)
	}
	return strings.Join(parts, " AND ")
}
