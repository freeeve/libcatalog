package httpapi

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/freeeve/libcat/ingest"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/backend/workindex"
)

// schemeResolver adapts the vocab index (nil-safe) into the facet counter's
// IRI -> scheme hook.
func schemeResolver(vx *vocab.Index) func(string) string {
	if vx == nil {
		return nil
	}
	return func(iri string) string {
		if t, ok := vx.Resolve(iri); ok {
			return t.Scheme
		}
		return ""
	}
}

// tombstoneMode turns the ?tombstoned= parameter into the predicate that decides
// which summaries the works list is even searching.
//
// The default is exclude. A tombstone says "this record is retired; here is where
// it went", so a cataloger searching for a book is not looking for one -- and on
// a catalog that has retired anything at all, the retired records bury the live
// ones. They stay reachable: "include" shows them alongside, and
// "only" is the audit question, "what did I retire?".
//
// An unrecognized value is refused rather than silently treated as the default: a
// client that meant "only" and got "exclude" would be shown an empty list and
// conclude the records were gone.
func tombstoneMode(raw string) (keep func(ingest.WorkSummary) bool, ok bool) {
	switch raw {
	case "", "exclude":
		return func(s ingest.WorkSummary) bool { return !s.Tombstoned }, true
	case "include":
		return func(ingest.WorkSummary) bool { return true }, true
	case "only":
		return func(s ingest.WorkSummary) bool { return s.Tombstoned }, true
	}
	return nil, false
}

// worksList serves the SPA's work search off the shared work index, which
// carries the freshness contract this cache used to own (fresh after a
// publish, not per-keystroke scans).
type worksList struct {
	ix *workindex.Index
	// extraFacets are the extras keys served as facet groups,
	// reserved parameter names already filtered out.
	extraFacets []string
}

func (wl *worksList) summaries(r *http.Request) ([]ingest.WorkSummary, error) {
	return wl.ix.Summaries(r.Context())
}

// registerWorksList mounts GET /v1/works?q=&limit=&offset=&tombstoned= (librarian)
// and returns the shared summary source for sibling endpoints (tags typeahead).
// The response carries total (the catalog under the current tombstoned mode) and
// matched (query hits) so the SPA can page: works is the [offset, offset+limit)
// window of matches. total counts what a query with no terms would match, so
// "3 of 41" never says 41 while offering 3 pages of one.
// extraFacets names the extras keys served as additional facet groups
// ; a key shadowing a built-in parameter is dropped with a
// warning rather than silently swallowing that parameter. vx (nil when no
// vocabularies are installed) resolves subject IRIs to their vocabulary
// scheme, so the rail can group subjects per authority.
func registerWorksList(mux *http.ServeMux, ix *workindex.Index, verifier auth.TokenVerifier, extraFacets []string, vx *vocab.Index) *worksList {
	wl := &worksList{ix: ix}
	for _, key := range extraFacets {
		if reservedWorkParams[key] {
			slog.Warn("httpapi: extras facet shadows a built-in works parameter; skipping", "key", key)
			continue
		}
		wl.extraFacets = append(wl.extraFacets, key)
	}
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	mux.Handle("GET /v1/works", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		all, err := wl.summaries(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		params := r.URL.Query()
		q := strings.ToLower(strings.TrimSpace(params.Get("q")))
		showTombstoned, ok := tombstoneMode(params.Get("tombstoned"))
		if !ok {
			writeError(w, http.StatusBadRequest, "tombstoned must be exclude|include|only")
			return
		}
		limit := 50
		if raw := params.Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
				limit = n
			}
		}
		offset := 0
		if raw := params.Get("offset"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
				offset = n
			}
		}
		// Facets: filters AND across groups, OR within one;
		// counts are self-excluding over the query-matched set.
		groups := workFacetGroups(params, wl.extraFacets, schemeResolver(vx))
		counter := newFacetCounter(groups)
		matched := 0
		total := 0
		matches := make([]ingest.WorkSummary, 0, limit)
		for _, s := range all {
			// Before the query, before the facets: a retired record is not part
			// of the set being searched unless it was asked for. Filtering here
			// rather than in the client keeps matched, the paging window and the
			// facet counts describing the same set of works.
			if !showTombstoned(s) {
				continue
			}
			total++
			if q != "" && !s.Matches(q) {
				continue
			}
			m := groupMatches(groups, s)
			counter.add(s, m)
			pass := true
			for _, ok := range m {
				pass = pass && ok
			}
			if !pass {
				continue
			}
			matched++
			if matched <= offset || len(matches) >= limit {
				continue
			}
			matches = append(matches, s)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"works":   matches,
			"total":   total,
			"matched": matched,
			"offset":  offset,
			"facets":  counter.result(),
		})
	})))
	return wl
}
