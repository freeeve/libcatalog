package httpapi

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/copycat"
	"github.com/freeeve/libcat/backend/marcview"
	"github.com/freeeve/libcat/backend/vocab"
)

// subjectCandidate is one external heading a cataloger can pull in: the
// heading text, its MARC tag and source vocabulary, the $0 identifier URIs
// it carried, how many target records carried it, and -- when an identifier
// or the whole heading matches a loaded vocabulary -- the controlled term
// to add instead of a tag.
type subjectCandidate struct {
	Heading string         `json:"heading"`
	Tag     string         `json:"tag"`
	Source  string         `json:"source,omitempty"`
	IDs     []string       `json:"ids,omitempty"`
	Count   int            `json:"count"`
	Targets []string       `json:"targets"`
	Term    *vocab.TermRef `json:"term,omitempty"`
}

// registerSubjectLookup mounts the external-subject fetch: the
// work's ISBNs fan out to the copycat targets, 6XX headings come back
// deduped and reconciled against the local index. Explicitly button-driven
// -- target fan-out takes seconds.
func registerSubjectLookup(mux *http.ServeMux, cc *copycat.Service, bs blob.Store, ix *vocab.Index, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	mux.Handle("POST /v1/works/{id}/subjects/lookup", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		var req struct {
			Targets []string `json:"targets"`
		}
		if r.ContentLength > 0 {
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "bad request body")
				return
			}
		}
		grain, _, _, ok := readWorkGrain(w, r, bs)
		if !ok {
			return
		}
		summaries, err := ingest.SummarizeGrain(grain)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain parse failed")
			return
		}
		var isbns []string
		existingSubjects := map[string]bool{}
		existingLabels := map[string]bool{}
		for _, summary := range summaries {
			if summary.WorkID != workID {
				continue
			}
			isbns = append(isbns, summary.ISBNs...)
			for _, s := range summary.Subjects {
				existingSubjects[s] = true
			}
			for _, tag := range summary.Tags {
				existingLabels[normHeading(tag)] = true
			}
		}
		if len(isbns) > 5 {
			isbns = isbns[:5]
		}
		if len(isbns) == 0 {
			writeError(w, http.StatusBadRequest, "this work carries no ISBNs to search by")
			return
		}
		byKey := map[string]*subjectCandidate{}
		failures := map[string]string{}
		warnings := map[string]string{}
		for _, isbn := range isbns {
			results, fails, warns, err := cc.SearchAll(r.Context(), "", []copycat.FieldTerm{{Index: "isbn", Term: isbn}}, req.Targets)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			maps.Copy(failures, fails)
			maps.Copy(warnings, warns)
			for _, res := range results {
				collectSubjects(byKey, res.Target, res.Record)
			}
		}
		candidates := make([]subjectCandidate, 0, len(byKey))
		for _, c := range byKey {
			if existingLabels[normHeading(c.Heading)] {
				continue
			}
			if ix != nil {
				c.Term = reconcileIdentifiers(ix, c.IDs)
				if c.Term == nil {
					c.Term = reconcileHeading(ix, c.Heading)
				}
				if c.Term != nil && existingSubjects[c.Term.ID] {
					continue
				}
			}
			sort.Strings(c.Targets)
			candidates = append(candidates, *c)
		}
		// Controlled matches first, then by prevalence.
		sort.Slice(candidates, func(i, j int) bool {
			if (candidates[i].Term != nil) != (candidates[j].Term != nil) {
				return candidates[i].Term != nil
			}
			if candidates[i].Count != candidates[j].Count {
				return candidates[i].Count > candidates[j].Count
			}
			return candidates[i].Heading < candidates[j].Heading
		})
		writeJSON(w, http.StatusOK, map[string]any{"candidates": candidates, "failures": failures, "warnings": warnings})
	})))

	// Identifier kinds: each bf:identifiedBy value mapped to its
	// BIBFRAME type so the editor can badge ISBN vs ISSN vs provider id.
	mux.Handle("GET /v1/works/{id}/identifiers", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grain, _, workID, ok := readWorkGrain(w, r, bs)
		if !ok {
			return
		}
		gi, err := identity.ScanGrain(grain)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain parse failed")
			return
		}
		kinds := map[string]string{}
		for _, inst := range gi.Instances {
			for _, pk := range inst.ProviderKeys {
				if scheme, value, ok := strings.Cut(pk, ":"); ok {
					if _, taken := kinds[value]; !taken {
						kinds[value] = scheme
					}
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"workId": workID, "kinds": kinds})
	})))
}

// subjectTags are the MARC 6XX fields worth harvesting; ind2 names the
// source vocabulary (7 defers to $2).
var subjectTags = map[string]bool{
	"600": true, "610": true, "611": true, "630": true,
	"648": true, "650": true, "651": true, "655": true,
}

var ind2Sources = map[string]string{"0": "lcsh", "1": "lcshac", "2": "mesh", "5": "cash", "6": "rvm"}

func collectSubjects(byKey map[string]*subjectCandidate, target string, rec marcview.RecordDoc) {
	for _, f := range rec.Fields {
		if !subjectTags[f.Tag] {
			continue
		}
		heading, source, ids := headingOf(f)
		if heading == "" {
			continue
		}
		key := f.Tag + "|" + normHeading(heading)
		c := byKey[key]
		if c == nil {
			c = &subjectCandidate{Heading: heading, Tag: f.Tag, Source: source}
			byKey[key] = c
		}
		for _, id := range ids {
			if !slices.Contains(c.IDs, id) {
				c.IDs = append(c.IDs, id)
			}
		}
		c.Count++
		found := false
		for _, t := range c.Targets {
			if t == target {
				found = true
			}
		}
		if !found {
			c.Targets = append(c.Targets, target)
		}
	}
}

// headingOf joins a 6XX field into a display heading: name/title subfields
// space-joined, subdivisions ($v$x$y$z) double-dash-joined, trailing
// punctuation trimmed. Returns the heading, its source vocabulary, and the
// resolvable identifier URIs its $0 subfields carry.
func headingOf(f marcview.Field) (string, string, []string) {
	var main []string
	var subs []string
	var ids []string
	source := ind2Sources[f.Ind2]
	for _, sf := range f.Subfields {
		switch sf.Code {
		case "a", "b", "c", "d", "t":
			main = append(main, strings.TrimSpace(sf.Value))
		case "v", "x", "y", "z":
			subs = append(subs, strings.TrimSpace(sf.Value))
		case "0":
			if id := subjectIDURI(sf.Value); id != "" && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		case "2":
			if source == "" {
				source = sf.Value
			}
		}
	}
	heading := strings.Join(main, " ")
	if len(subs) > 0 {
		heading += "--" + strings.Join(subs, "--")
	}
	return strings.TrimRight(strings.TrimSpace(heading), ".,"), source, ids
}

// subjectIDURI folds a 6XX $0 value into a resolvable identifier URI: full
// http(s) URIs pass through, the "(DE-588)X" GND parenthetical form becomes
// its d-nb.info URI. Other control numbers (local PPNs, bare LCCNs) return
// "" -- nothing loadable claims them.
func subjectIDURI(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
		return v
	}
	if id, ok := strings.CutPrefix(v, "(DE-588)"); ok && id != "" {
		return "https://d-nb.info/gnd/" + strings.TrimSpace(id)
	}
	return ""
}

// reconcileIdentifiers resolves the first $0 identifier any loaded term
// claims as its own URI or a skos exact/close match sibling. Identifier
// matches outrank label matches: they survive the language gap (a German
// GND heading still lands on the English-labeled term).
func reconcileIdentifiers(ix *vocab.Index, ids []string) *vocab.TermRef {
	for _, id := range ids {
		if t, ok := ix.MatchIdentifier(id); ok {
			return &vocab.TermRef{Scheme: t.Scheme, ID: t.ID, Label: t.Label("en")}
		}
	}
	return nil
}

func normHeading(s string) string {
	return strings.TrimSuffix(strings.Join(strings.Fields(strings.ToLower(s)), " "), ".")
}

// reconcileHeading whole-heading-matches against every loaded scheme: the
// full heading first, then its pre-subdivision head.
func reconcileHeading(ix *vocab.Index, heading string) *vocab.TermRef {
	tries := []string{heading}
	if head, _, ok := strings.Cut(heading, "--"); ok {
		tries = append(tries, head)
	}
	for _, try := range tries {
		for _, scheme := range ix.Schemes() {
			for _, m := range ix.MatchLabel(scheme, try) {
				if m.Term.MergedInto == "" {
					return &vocab.TermRef{Scheme: scheme, ID: m.Term.ID, Label: m.Term.Label("en")}
				}
			}
		}
	}
	return nil
}
