// Faceted filters over the works list (tasks/168): cataloger-shaped slices
// of the workindex summaries -- visibility, holdings, completeness gaps,
// controlled subjects, and raw tags. Filters AND across groups and OR
// within one; each group's counts are computed with every other group's
// filters applied (self-excluding), the standard facet UX. Everything
// derives from fields the summary already carries -- no grain reads and no
// summary-format change (the persisted snapshot stays compatible).
package httpapi

import (
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/freeeve/libcat/ingest"
)

// facetCount is one value of one facet group with its work count.
type facetCount struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

// workFilters carries one request's facet selections, keyed by group.
type workFilters struct {
	visibility []string
	holdings   []string
	needs      []string
	subjects   []string
	tags       []string
}

func parseWorkFilters(q url.Values) workFilters {
	return workFilters{
		visibility: q["visibility"],
		holdings:   q["holdings"],
		needs:      q["needs"],
		subjects:   q["subject"],
		tags:       q["tag"],
	}
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

// facetGroups is the group evaluation order; index positions match the
// membership arrays below.
var facetGroups = []string{"visibility", "holdings", "needs", "subject", "tag"}

// groupMatches reports, per group, whether the summary passes that group's
// filter (an empty selection always passes).
func (f workFilters) groupMatches(s ingest.WorkSummary) [5]bool {
	anyOf := func(have []string, want []string) bool {
		if len(want) == 0 {
			return true
		}
		for _, w := range want {
			if slices.Contains(have, w) {
				return true
			}
		}
		return false
	}
	var m [5]bool
	m[0] = anyOf([]string{visibilityOf(s)}, f.visibility)
	m[1] = anyOf(holdingsOf(s), f.holdings)
	m[2] = anyOf(needsOf(s), f.needs)
	m[3] = anyOf(s.Subjects, f.subjects)
	tags := make([]string, len(s.Tags))
	for i, t := range s.Tags {
		tags[i] = strings.ToLower(t)
	}
	m[4] = anyOf(tags, lowerAll(f.tags))
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
	counts [5]map[string]int
}

func newFacetCounter() *facetCounter {
	c := &facetCounter{}
	for i := range c.counts {
		c.counts[i] = map[string]int{}
	}
	return c
}

// add folds one q-matched summary in: a group counts the work when every
// OTHER group's filter passes it.
func (c *facetCounter) add(s ingest.WorkSummary, m [5]bool) {
	othersPass := func(g int) bool {
		for i, ok := range m {
			if i != g && !ok {
				return false
			}
		}
		return true
	}
	if othersPass(0) {
		c.counts[0][visibilityOf(s)]++
	}
	if othersPass(1) {
		for _, v := range holdingsOf(s) {
			c.counts[1][v]++
		}
	}
	if othersPass(2) {
		for _, v := range needsOf(s) {
			c.counts[2][v]++
		}
	}
	if othersPass(3) {
		for _, v := range s.Subjects {
			c.counts[3][v]++
		}
	}
	if othersPass(4) {
		for _, v := range s.Tags {
			c.counts[4][strings.ToLower(v)]++
		}
	}
}

// facetTopN bounds the open-ended groups (subjects, tags) in the response.
const facetTopN = 20

// result renders the counts: fixed-vocabulary groups list every nonzero
// value; subjects and tags list the top facetTopN by count then value.
func (c *facetCounter) result() map[string][]facetCount {
	out := map[string][]facetCount{}
	for i, group := range facetGroups {
		list := make([]facetCount, 0, len(c.counts[i]))
		for v, n := range c.counts[i] {
			list = append(list, facetCount{Value: v, Count: n})
		}
		sort.Slice(list, func(a, b int) bool {
			if list[a].Count != list[b].Count {
				return list[a].Count > list[b].Count
			}
			return list[a].Value < list[b].Value
		})
		if (group == "subject" || group == "tag") && len(list) > facetTopN {
			list = list[:facetTopN]
		}
		out[group] = list
	}
	return out
}
