// Faceted filters over the works list (tasks/168): cataloger-shaped slices
// of the workindex summaries -- visibility, holdings, completeness gaps,
// controlled subjects, raw tags, and configured extras dimensions like
// provenance sources (tasks/171). Filters AND across groups and OR within
// one; each group's counts are computed with every other group's filters
// applied (self-excluding), the standard facet UX. Everything derives from
// fields the summary already carries -- no grain reads.
package httpapi

import (
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/freeeve/libcat/ingest"
)

// facetCount is one value of one facet group with its work count. Scheme is
// set only on controlled-subject values (tasks/174): the vocabulary the IRI
// resolves to, so the rail can group per vocabulary and name the authority.
type facetCount struct {
	Value  string `json:"value"`
	Count  int    `json:"count"`
	Scheme string `json:"scheme,omitempty"`
}

// facetGroup is one facet dimension of a request: its response key (also the
// query parameter it filters by), the requested filter values, and how a
// summary buckets into it.
type facetGroup struct {
	name     string
	selected []string
	valuesOf func(ingest.WorkSummary) []string
	fold     bool // case-insensitive value matching (tags)
	capped   bool // top-N response cap for open-ended vocabularies
	// schemeOf, when set, annotates each value with its vocabulary scheme,
	// and the top-N cap applies per scheme so a large vocabulary cannot
	// crowd a smaller one out of the rail (tasks/174).
	schemeOf func(string) string
}

// reservedWorkParams are the works-list query parameters extras facets may
// not shadow: the built-in groups plus the paging/search params.
var reservedWorkParams = map[string]bool{
	"visibility": true, "holdings": true, "needs": true, "subject": true,
	"tag": true, "q": true, "limit": true, "offset": true,
	"tombstoned": true,
}

// workFacetGroups assembles one request's facet groups: the five built-ins,
// then one group per configured extras key (tasks/171) bucketing on the
// summary's comma-split extras value. schemeOf (nil-safe) resolves a subject
// IRI to its vocabulary scheme for per-vocabulary grouping (tasks/174).
func workFacetGroups(q url.Values, extras []string, schemeOf func(string) string) []facetGroup {
	groups := []facetGroup{
		{name: "visibility", selected: q["visibility"], valuesOf: func(s ingest.WorkSummary) []string { return []string{visibilityOf(s)} }},
		{name: "holdings", selected: q["holdings"], valuesOf: holdingsOf},
		{name: "needs", selected: q["needs"], valuesOf: needsOf},
		{name: "subject", selected: q["subject"], valuesOf: func(s ingest.WorkSummary) []string { return s.Subjects }, capped: true, schemeOf: schemeOf},
		{name: "tag", selected: q["tag"], valuesOf: func(s ingest.WorkSummary) []string { return s.Tags }, fold: true, capped: true},
	}
	for _, key := range extras {
		groups = append(groups, facetGroup{
			name:     key,
			selected: q[key],
			valuesOf: func(s ingest.WorkSummary) []string { return splitExtra(s.Extras[key]) },
			capped:   true,
		})
	}
	return groups
}

// splitExtra splits a comma-joined extras value into trimmed facet values --
// the lcat convention for multi-valued extras (the hugo module's extraFacets
// split), a single-valued extra passing through unchanged.
func splitExtra(v string) []string {
	var out []string
	for p := range strings.SplitSeq(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// visibilityOf buckets a summary into exactly one visibility value: what
// the public projection does with it. A withdrawn-but-kept work is public
// by the curator's decision (tasks/078).
func visibilityOf(s ingest.WorkSummary) string {
	switch {
	case s.Tombstoned:
		return "tombstoned"
	case s.Suppressed:
		return "suppressed"
	case s.Withdrawn != "" && !s.Kept:
		return "withdrawn"
	default:
		return "public"
	}
}

// holdingsOf lists the holdings signals a summary carries (multi-valued).
func holdingsOf(s ingest.WorkSummary) []string {
	var out []string
	if s.Items > 0 {
		out = append(out, "physical")
	}
	if s.HasAvailability {
		out = append(out, "digital")
	}
	if len(out) == 0 {
		out = append(out, "none")
	}
	return out
}

// needsOf lists a summary's completeness gaps (multi-valued): the triage
// slices catalogers work through.
func needsOf(s ingest.WorkSummary) []string {
	var out []string
	if len(s.Subjects) == 0 {
		out = append(out, "subjects")
	}
	if len(s.Contributors) == 0 {
		out = append(out, "contributors")
	}
	if len(s.ISBNs) == 0 {
		out = append(out, "isbn")
	}
	return out
}

// groupMatches reports, per group, whether the summary passes that group's
// filter (an empty selection always passes).
func groupMatches(groups []facetGroup, s ingest.WorkSummary) []bool {
	m := make([]bool, len(groups))
	for i, g := range groups {
		if len(g.selected) == 0 {
			m[i] = true
			continue
		}
		have, want := g.valuesOf(s), g.selected
		if g.fold {
			have, want = lowerAll(have), lowerAll(want)
		}
		for _, w := range want {
			if slices.Contains(have, w) {
				m[i] = true
				break
			}
		}
	}
	return m
}

func lowerAll(vs []string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strings.ToLower(v)
	}
	return out
}

// facetCounter accumulates self-excluding counts across one scan.
type facetCounter struct {
	groups []facetGroup
	counts []map[string]int
}

func newFacetCounter(groups []facetGroup) *facetCounter {
	c := &facetCounter{groups: groups, counts: make([]map[string]int, len(groups))}
	for i := range c.counts {
		c.counts[i] = map[string]int{}
	}
	return c
}

// add folds one q-matched summary in: a group counts the work when every
// OTHER group's filter passes it.
func (c *facetCounter) add(s ingest.WorkSummary, m []bool) {
	for g, group := range c.groups {
		othersPass := true
		for i, ok := range m {
			if i != g && !ok {
				othersPass = false
				break
			}
		}
		if !othersPass {
			continue
		}
		for _, v := range group.valuesOf(s) {
			if group.fold {
				v = strings.ToLower(v)
			}
			c.counts[g][v]++
		}
	}
}

// facetTopN bounds the open-ended groups (subjects, tags, extras) in the
// response.
const facetTopN = 20

// result renders the counts: fixed-vocabulary groups list every nonzero
// value; open-ended groups list the top facetTopN by count then value. A
// scheme-annotated group (subjects, tasks/174) caps per scheme instead, so
// every vocabulary keeps a presence in the rail.
func (c *facetCounter) result() map[string][]facetCount {
	out := map[string][]facetCount{}
	for i, group := range c.groups {
		list := make([]facetCount, 0, len(c.counts[i]))
		for v, n := range c.counts[i] {
			fc := facetCount{Value: v, Count: n}
			if group.schemeOf != nil {
				fc.Scheme = group.schemeOf(v)
			}
			list = append(list, fc)
		}
		sort.Slice(list, func(a, b int) bool {
			if list[a].Count != list[b].Count {
				return list[a].Count > list[b].Count
			}
			return list[a].Value < list[b].Value
		})
		if group.capped {
			if group.schemeOf != nil {
				list = capPerScheme(list, facetTopN)
			} else if len(list) > facetTopN {
				list = list[:facetTopN]
			}
		}
		out[group.name] = list
	}
	return out
}

// capPerScheme keeps the top n values of each scheme, preserving the overall
// count-then-value order.
func capPerScheme(list []facetCount, n int) []facetCount {
	kept := make([]facetCount, 0, len(list))
	perScheme := map[string]int{}
	for _, fc := range list {
		if perScheme[fc.Scheme] >= n {
			continue
		}
		perScheme[fc.Scheme]++
		kept = append(kept, fc)
	}
	return kept
}
