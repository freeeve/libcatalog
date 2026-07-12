package httpapi

import (
	"net/http"
	"sort"
	"strconv"

	"github.com/freeeve/libcat/project"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/backend/workindex"
)

// auditTerm is one subject term as the facet builder sees it: the identifier
// (URI for controlled subjects, the text itself for headings and tags), a
// display label when one resolves, the vocabulary scheme, and how many works
// in scope carry it.
type auditTerm struct {
	URI    string `json:"uri,omitempty"`
	Label  string `json:"label,omitempty"`
	Scheme string `json:"scheme,omitempty"`
	Works  int    `json:"works"`
}

// auditTermsDefaultLimit bounds each list in the histogram response; the long
// tail of once-used terms rarely informs a facet, and the response says how
// much it truncated so nothing silently disappears.
const auditTermsDefaultLimit = 500

// registerAuditTerms mounts the subject-term histogram behind the crosswalk
// editor's facet builder: every controlled subject URI, heading label, and
// tag in the (optionally filtered) corpus with its work count, so an operator
// picks facet members from what the collection actually holds instead of
// hand-typing URIs. Labels resolve through the vocabulary index when one is
// loaded; unresolved URIs still return, just unlabeled.
func registerAuditTerms(mux *http.ServeMux, ix *workindex.Index, vix *vocab.Index, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	mux.Handle("GET /v1/audit/terms", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filters, err := auditFilters(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		limit := auditTermsDefaultLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > 5000 {
				writeError(w, http.StatusBadRequest, "limit wants 1-5000")
				return
			}
			limit = n
		}
		sums, _, err := ix.SummariesWithGeneration(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, errScanFailed.Error())
			return
		}
		total := 0
		uriCounts := map[string]int{}
		headingCounts := map[string]int{}
		tagCounts := map[string]int{}
		for i := range sums {
			s := &sums[i]
			if !includeInAudit(s, filters) {
				continue
			}
			total++
			for _, u := range dedupe(s.Subjects) {
				uriCounts[u]++
			}
			for _, h := range dedupe(s.Headings) {
				headingCounts[h]++
			}
			for _, t := range dedupe(s.Tags) {
				tagCounts[t]++
			}
		}
		uris, uriTotal := topTerms(uriCounts, limit, func(u string) auditTerm {
			t := auditTerm{URI: u, Scheme: project.SchemeForURI(u)}
			if vix != nil {
				if vt, ok := vix.Resolve(u); ok {
					t.Label = vt.Label("en")
					if vt.Scheme != "" {
						t.Scheme = vt.Scheme
					}
				}
			}
			return t
		})
		headings, headingTotal := topTerms(headingCounts, limit, func(h string) auditTerm {
			return auditTerm{Label: h}
		})
		tags, tagTotal := topTerms(tagCounts, limit, func(t string) auditTerm {
			return auditTerm{Label: t}
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"totalWorks":   total,
			"uris":         uris,
			"uriTotal":     uriTotal,
			"headings":     headings,
			"headingTotal": headingTotal,
			"tags":         tags,
			"tagTotal":     tagTotal,
			"scope":        filters.String(),
		})
	})))
}

// topTerms renders a count map as terms sorted by descending work count (label
// as tiebreak), truncated to limit; the second return is the untruncated
// distinct-term count so the caller can say what was dropped.
func topTerms(counts map[string]int, limit int, mk func(string) auditTerm) ([]auditTerm, int) {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] != counts[keys[j]] {
			return counts[keys[i]] > counts[keys[j]]
		}
		return keys[i] < keys[j]
	})
	n := len(keys)
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]auditTerm, 0, len(keys))
	for _, k := range keys {
		t := mk(k)
		t.Works = counts[k]
		out = append(out, t)
	}
	return out, n
}

// dedupe returns vals with duplicates removed, preserving order; a work's
// repeated heading must count it once.
func dedupe(vals []string) []string {
	if len(vals) < 2 {
		return vals
	}
	seen := make(map[string]bool, len(vals))
	out := vals[:0:0]
	for _, v := range vals {
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}
