package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/freeeve/libcat/diversity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/project"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/workindex"
)

// auditResponse is the diversity report plus what it was computed over, matching
// the `lcat audit` JSON shape so a saved report reads the same either way. The
// creators block aggregates the cached wikidata claims when any exist.
type auditResponse struct {
	Input string `json:"input"`
	Scope string `json:"scope,omitempty"`
	diversity.Report
	Creators *creatorAudit `json:"creators,omitempty"`
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
func registerAudit(mux *http.ServeMux, ix *workindex.Index, verifier auth.TokenVerifier, cws *crosswalkSource) func(*http.Request) (auditResponse, auditFilterSet, int, error) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	cache := &auditCache{}

	compute := func(r *http.Request) (auditResponse, auditFilterSet, int, error) {
		filters, err := auditFilters(r)
		if err != nil {
			return auditResponse{}, nil, http.StatusBadRequest, err
		}
		sums, gen, err := ix.SummariesWithGeneration(r.Context())
		if err != nil {
			return auditResponse{}, nil, http.StatusInternalServerError, errScanFailed
		}
		cw, cwHash, _, err := cws.effective(r)
		if err != nil {
			return auditResponse{}, nil, http.StatusInternalServerError, errScanFailed
		}
		key := cwHash + "\x00" + filters.cacheKey()
		if resp, ok := cache.get(gen, key); ok {
			return resp, filters, http.StatusOK, nil
		}
		a := diversity.NewAuditor(cw)
		include := func(s *ingest.WorkSummary) bool {
			return includeInAudit(s, filters)
		}
		for i := range sums {
			s := &sums[i]
			if !include(s) {
				continue
			}
			a.Add(summaryRefs(s))
		}
		resp := auditResponse{
			Input:    "work index (cataloging corpus: suppressed included, tombstoned excluded)",
			Scope:    filters.String(),
			Report:   a.Report(),
			Creators: aggregateCreators(sums, include),
		}
		cache.put(gen, key, resp)
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
// extra (the sources convention) matches on any element.
func (f auditFilterSet) match(extra map[string]string) bool {
	for _, p := range f {
		got, ok := extra[p[0]]
		if !ok {
			return false
		}
		if got == p[1] {
			continue
		}
		found := false
		for _, part := range strings.Split(got, ",") {
			if strings.TrimSpace(part) == p[1] {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// summaryRefs turns a work summary's aboutness signal into audit refs: controlled
// subject IRIs (scheme from the URI namespace), their heading labels, and the
// uncontrolled tags -- the same three dimensions the CLI's json and graph inputs
// feed, so all three surfaces measure the same thing.
func summaryRefs(s *ingest.WorkSummary) []diversity.SubjectRef {
	refs := make([]diversity.SubjectRef, 0, len(s.Subjects)+len(s.Headings)+len(s.Tags))
	for _, uri := range s.Subjects {
		refs = append(refs, diversity.SubjectRef{URI: uri, Scheme: project.SchemeForURI(uri)})
	}
	for _, h := range s.Headings {
		refs = append(refs, diversity.SubjectRef{Labels: []string{h}})
	}
	for _, t := range s.Tags {
		refs = append(refs, diversity.SubjectRef{Labels: []string{t}})
	}
	return refs
}
