// Facet dimensions over a WorkSummary.
//
// These live next to WorkSummary rather than in the HTTP layer because two
// callers must agree on them exactly: the works listing, which counts and
// filters facets for the rail, and a batch/export selection, which resolves the
// same filters into a set of Works. When they drifted, "Export these results…"
// offered the entire catalog. One definition, two callers.
package ingest

import (
	"slices"
	"strings"
)

// Facet group names. Anything else is an extras key.
const (
	FacetVisibility = "visibility"
	FacetHoldings   = "holdings"
	FacetNeeds      = "needs"
	FacetSubject    = "subject"
	FacetTag        = "tag"
)

// Visibility buckets a summary into exactly one value: what the public
// projection does with it. A withdrawn-but-kept Work is public by the curator's
// decision.
func Visibility(s WorkSummary) string {
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

// Holdings lists the holdings signals a summary carries (multi-valued).
func Holdings(s WorkSummary) []string {
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

// Needs lists a summary's completeness gaps (multi-valued): the triage slices
// catalogers work through.
func Needs(s WorkSummary) []string {
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

// FacetValues returns a summary's values in one facet group. An unrecognized
// group name is read as an extras key, whose comma-joined value is split -- the
// lcat convention for multi-valued extras.
func FacetValues(s WorkSummary, group string) []string {
	switch group {
	case FacetVisibility:
		return []string{Visibility(s)}
	case FacetHoldings:
		return Holdings(s)
	case FacetNeeds:
		return Needs(s)
	case FacetSubject:
		return s.Subjects
	case FacetTag:
		return s.Tags
	}
	return SplitExtra(s.Extras[group])
}

// FoldsCase reports whether a group's values match case-insensitively. Tags are
// free text a cataloger typed; the controlled dimensions are not.
func FoldsCase(group string) bool { return group == FacetTag }

// SplitExtra splits a comma-joined extras value into trimmed facet values. A
// single-valued extra passes through unchanged.
func SplitExtra(v string) []string {
	var out []string
	for p := range strings.SplitSeq(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// MatchesGroup reports whether a summary passes one group's filter. An empty
// selection always passes: an unselected facet means "any", not "none".
func MatchesGroup(s WorkSummary, group string, selected []string) bool {
	if len(selected) == 0 {
		return true
	}
	have := FacetValues(s, group)
	if FoldsCase(group) {
		have, selected = lowerAll(have), lowerAll(selected)
	}
	for _, want := range selected {
		if slices.Contains(have, want) {
			return true
		}
	}
	return false
}

// MatchesFacets reports whether a summary passes every group's filter: AND
// across groups, OR within one. This is the rule the facet rail draws and the
// rule an export selection must resolve by, or the count beside the button and
// the count in the file disagree.
func MatchesFacets(s WorkSummary, filters map[string][]string) bool {
	for group, selected := range filters {
		if !MatchesGroup(s, group, selected) {
			return false
		}
	}
	return true
}

func lowerAll(vs []string) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = strings.ToLower(v)
	}
	return out
}
