package httpapi

import (
	"net/http"
	"strconv"

	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

const termsDefaultLimit = 20

// registerTerms mounts the public autocomplete endpoint over the vocabulary
// index. With the suggestion service present, scheme=folk serves ACCEPTED
// community tags (PROPOSED and BLOCKED terms stay invisible).
func registerTerms(mux *http.ServeMux, ix *vocab.Index, folk *suggest.Service) {
	// Batch scheme-agnostic resolve: the editor's subject chips
	// turn stored URIs into labels without knowing the scheme. Unresolvable
	// URIs are absent from the response, never errors.
	mux.HandleFunc("GET /v1/terms/resolve", func(w http.ResponseWriter, r *http.Request) {
		ids := r.URL.Query()["id"]
		if len(ids) == 0 || len(ids) > 100 {
			writeError(w, http.StatusBadRequest, "1-100 id parameters")
			return
		}
		terms := map[string]*vocab.Term{}
		for _, id := range ids {
			if t, ok := ix.Resolve(id); ok {
				terms[id] = t
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"terms": terms})
	})

	// Single-term lookup: the picker's neighborhood panel resolves
	// broader/narrower/related URIs to full terms through this.
	mux.HandleFunc("GET /v1/term", func(w http.ResponseWriter, r *http.Request) {
		scheme := r.URL.Query().Get("scheme")
		id := r.URL.Query().Get("id")
		term, ok := ix.Lookup(scheme, id)
		if !ok {
			writeError(w, http.StatusNotFound, "unknown term")
			return
		}
		writeJSON(w, http.StatusOK, term)
	})

	mux.HandleFunc("GET /v1/terms", func(w http.ResponseWriter, r *http.Request) {
		scheme := r.URL.Query().Get("scheme")
		q := r.URL.Query().Get("q")
		if scheme == "" {
			schemes := ix.Schemes()
			if folk != nil {
				schemes = append(schemes, vocab.FolkScheme)
			}
			writeJSON(w, http.StatusOK, map[string]any{"schemes": schemes})
			return
		}
		limit := termsDefaultLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 100 {
				limit = n
			}
		}
		if scheme == vocab.FolkScheme {
			if folk == nil {
				writeJSON(w, http.StatusOK, map[string]any{"terms": []any{}})
				return
			}
			norm, err := vocab.NormalizeFolk(q)
			if err != nil {
				norm = ""
			}
			names, err := folk.AcceptedFolkTerms(r.Context(), norm, limit)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "folk lookup failed")
				return
			}
			terms := make([]vocab.TermRef, 0, len(names))
			for _, name := range names {
				terms = append(terms, vocab.TermRef{Scheme: vocab.FolkScheme, ID: name, Label: name})
			}
			writeJSON(w, http.StatusOK, map[string]any{"terms": terms})
			return
		}
		// Each hit carries its broader-chain path so the picker
		// can show where a term sits without extra lookups.
		type termHit struct {
			*vocab.Term
			Path []vocab.TermRef `json:"path,omitempty"`
		}
		hits := []termHit{}
		for _, t := range ix.Search(scheme, q, limit) {
			hits = append(hits, termHit{Term: t, Path: ix.Path(scheme, t.ID)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"terms": hits})
	})
}
