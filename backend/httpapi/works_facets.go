// Faceted filters over the works list: cataloger-shaped slices
// of the workindex summaries -- visibility, holdings, completeness gaps,
// controlled subjects, raw tags, and configured extras dimensions like
// provenance sources. Filters AND across groups and OR within
// one; each group's counts are computed with every other group's filters
// applied (self-excluding), the standard facet UX. Everything derives from
// fields the summary already carries -- no grain reads.
package httpapi

import (
	"net/url"
	"sort"
	"strings"

	"github.com/freeeve/libcat/ingest"
)

// facetCount is one value of one facet group with its work count. Scheme is
// set only on controlled-subject values: the vocabulary the IRI
// resolves to, so the rail can group per vocabulary and name the authority.
type facetCount struct {
	Value  string `json:"value"`
	Count  int    `json:"count"`
	Scheme string `json:"scheme,omitempty"`
}

// facetGroup is one facet dimension of a request: its response key (also the
// query parameter it filters by) and the requested filter values. How a summary
// buckets into it is ingest.FacetValues.
type facetGroup struct {
	name     string
	selected []string
	fold     bool // case-insensitive value matching (tags)
	capped   bool // top-N response cap for open-ended vocabularies
	// schemeOf, when set, annotates each value with its vocabulary scheme,
	// and the top-N cap applies per scheme so a large vocabulary cannot
	// crowd a smaller one out of the rail.
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
// then one group per configured extras key bucketing on the
// summary's comma-split extras value. schemeOf (nil-safe) resolves a subject
// IRI to its vocabulary scheme for per-vocabulary grouping.
// The bucketing rules live in ingest (ingest/facets.go) so a batch/export
// selection resolves by exactly the rule the rail draws. This file
// keeps what is a listing concern: which groups a request asks for, the
// self-excluding counts, and the response cap.
func workFacetGroups(q url.Values, extras []string, schemeOf func(string) string) []facetGroup {
	groups := []facetGroup{
		{name: ingest.FacetVisibility, selected: q[ingest.FacetVisibility]},
		{name: ingest.FacetHoldings, selected: q[ingest.FacetHoldings]},
		{name: ingest.FacetNeeds, selected: q[ingest.FacetNeeds]},
		{name: ingest.FacetSubject, selected: q[ingest.FacetSubject], capped: true, schemeOf: schemeOf},
		{name: ingest.FacetTag, selected: q[ingest.FacetTag], capped: true},
	}
	for _, key := range extras {
		groups = append(groups, facetGroup{name: key, selected: q[key], capped: true})
	}
	for i := range groups {
		groups[i].fold = ingest.FoldsCase(groups[i].name)
	}
	return groups
}

// valuesOf buckets a summary into this group.
func (g facetGroup) valuesOf(s ingest.WorkSummary) []string {
	return ingest.FacetValues(s, g.name)
}

// selectedFilters renders the request's active filters in the shape
// ingest.MatchesFacets takes.
func selectedFilters(groups []facetGroup) map[string][]string {
	out := map[string][]string{}
	for _, g := range groups {
		if len(g.selected) > 0 {
			out[g.name] = g.selected
		}
	}
	return out
}

// groupMatches reports, per group, whether the summary passes that group's
// filter (an empty selection always passes).
func groupMatches(groups []facetGroup, s ingest.WorkSummary) []bool {
	m := make([]bool, len(groups))
	for i, g := range groups {
		m[i] = ingest.MatchesGroup(s, g.name, g.selected)
	}
	return m
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
// scheme-annotated group (subjects) caps per scheme instead, so
// every vocabulary keeps a presence in the rail.
//
// A value the request selected is always listed, whatever its count and
// whatever the cap. The rail is the client's only account of what
// the query is: a filter that is applied but not displayed cannot be removed,
// and it silently corrupts every number read off that screen. Two ways a
// selection used to disappear, both covered here -- it ranks below the cap, or
// (because a group's counts honour every OTHER group's filter) it matches
// nothing at all under them and never reaches the counter.
func (c *facetCounter) result() map[string][]facetCount {
	out := map[string][]facetCount{}
	for i, group := range c.groups {
		list := make([]facetCount, 0, len(c.counts[i]))
		for v, n := range c.counts[i] {
			list = append(list, c.facet(group, v, n))
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
		out[group.name] = c.appendMissingSelections(list, i)
	}
	return out
}

// facet builds one response value, annotating its vocabulary scheme when the
// group carries one.
func (c *facetCounter) facet(group facetGroup, value string, count int) facetCount {
	fc := facetCount{Value: value, Count: count}
	if group.schemeOf != nil {
		fc.Scheme = group.schemeOf(value)
	}
	return fc
}

// selectedSet is the group's filter values keyed the way its counts are keyed
// (folded for case-insensitive groups), so a selection typed in any case pins
// the bucket it actually filters by.
func selectedSet(group facetGroup) map[string]bool {
	set := make(map[string]bool, len(group.selected))
	for _, v := range group.selected {
		if group.fold {
			v = strings.ToLower(v)
		}
		set[v] = true
	}
	return set
}

// capPerScheme keeps the top n values of each scheme, preserving the overall
// count-then-value order.
//
// The cap is deliberately not selection-aware: a truncated selection is put back
// by appendMissingSelections, which has to run anyway for selections that never
// reached the counter. One mechanism, not two that each half-cover the case.
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

// appendMissingSelections adds any selected value the scan never counted, with
// its true count of zero. Without this a selection that matches nothing under
// the other groups' filters is absent rather than merely truncated -- and zero
// is the honest answer, not a reason to hide the filter.
//
// The appended values are sorted so the response is a function of the request,
// not of Go's map iteration order.
func (c *facetCounter) appendMissingSelections(list []facetCount, i int) []facetCount {
	group := c.groups[i]
	present := make(map[string]bool, len(list))
	for _, fc := range list {
		present[fc.Value] = true
	}
	var missing []string
	for v := range selectedSet(group) {
		if !present[v] {
			missing = append(missing, v)
		}
	}
	sort.Strings(missing)
	for _, v := range missing {
		list = append(list, c.facet(group, v, c.counts[i][v]))
	}
	return list
}
