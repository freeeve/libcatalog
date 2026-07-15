package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"

	"github.com/freeeve/libcat/diversity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/vocab"
	"github.com/freeeve/libcat/backend/workindex"
)

// crosswalkBlobPath is where the operator's diversity-crosswalk override
// persists: the same TOML dialect `lcat diversity-audit --crosswalk` reads, so
// the file stays portable between the admin UI and the CLI.
const crosswalkBlobPath = "data/audit/crosswalk.toml"

// crosswalkSource loads the effective crosswalk (seed + persisted override) and
// memoizes the compiled form by override content, so the audit pays one blob
// read per request and a rebuild only when the override actually changed. The
// content hash doubles as the audit cache's crosswalk dimension, which keeps
// memoized reports honest across processes sharing the blob store.
type crosswalkSource struct {
	bs blob.Store
	// narrower resolves a term URI to its skos:narrower children (the
	// loaded vocab index), so root-defined facets expand at audit time.
	// Applied on every effective() return rather than cached: a vocabulary
	// reload must widen the closures without an override rewrite.
	narrower func(uri string) []string
	mu       sync.Mutex
	hash     string
	cw       *diversity.Crosswalk
}

// effective returns the compiled crosswalk plus the override's content hash
// and raw bytes. No override (blob not found) means the seed alone, hash "".
func (s *crosswalkSource) effective(r *http.Request) (*diversity.Crosswalk, string, []byte, error) {
	data, _, err := s.bs.Get(r.Context(), crosswalkBlobPath)
	if errors.Is(err, blob.ErrNotFound) {
		return diversity.Default().WithNarrower(s.narrower), "", nil, nil
	}
	if err != nil {
		return nil, "", nil, err
	}
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:8])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hash == hash && s.cw != nil {
		return s.cw.WithNarrower(s.narrower), hash, data, nil
	}
	cw, err := diversity.FromBytes(data)
	if err != nil {
		// A persisted override that no longer parses (hand-edited blob,
		// version skew) must not take the audit down; the seed still audits
		// and the config surface reports the broken document.
		return diversity.Default().WithNarrower(s.narrower), "", data, nil
	}
	s.hash, s.cw = hash, cw
	return cw.WithNarrower(s.narrower), hash, data, nil
}

// crosswalkBody is the PUT/preview request envelope: exactly one of categories
// (the structured editor's model) or toml (a pasted/uploaded crosswalk file).
type crosswalkBody struct {
	Categories []diversity.Category `json:"categories"`
	TOML       string               `json:"toml"`
}

// document validates the envelope and returns the override as canonical TOML
// bytes plus its parsed categories.
func (b crosswalkBody) document() ([]byte, []diversity.Category, error) {
	if (len(b.Categories) == 0) == (b.TOML == "") {
		return nil, nil, errors.New("body wants exactly one of categories or toml")
	}
	var doc []byte
	if b.TOML != "" {
		doc = []byte(b.TOML)
	} else {
		var err error
		if doc, err = diversity.EncodeCategories(b.Categories); err != nil {
			return nil, nil, err
		}
	}
	cats, err := diversity.ParseCategories(doc)
	if err != nil {
		return nil, nil, err
	}
	if _, err := diversity.FromBytes(doc); err != nil {
		return nil, nil, err
	}
	return doc, cats, nil
}

// crosswalkView is the GET response: the built-in seed, the operator's
// override (categories + raw TOML; absent when none), and the effective
// merged categories the audit runs.
type crosswalkView struct {
	Seed      []diversity.Category `json:"seed"`
	Override  []diversity.Category `json:"override,omitempty"`
	TOML      string               `json:"toml,omitempty"`
	Effective []diversity.Category `json:"effective"`
	// Broken carries the parse error of a persisted override that no longer
	// loads (the audit falls back to the seed until it is fixed or removed).
	Broken string `json:"broken,omitempty"`
}

// registerAuditCrosswalk mounts the operator crosswalk-configuration surface:
// GET/PUT/DELETE the persisted override, and POST preview to run the content
// audit with a candidate crosswalk WITHOUT persisting it -- the facet
// builder's live counts.
func registerAuditCrosswalk(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, vix *vocab.Index, auditLangs []string, verifier auth.TokenVerifier, cws *crosswalkSource) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	view := func(r *http.Request) (crosswalkView, error) {
		cw, _, raw, err := cws.effective(r)
		if err != nil {
			return crosswalkView{}, err
		}
		v := crosswalkView{Seed: diversity.Seed(), Effective: cw.Definitions()}
		if raw != nil {
			v.TOML = string(raw)
			cats, err := diversity.ParseCategories(raw)
			if err != nil {
				v.Broken = err.Error()
			} else {
				v.Override = cats
			}
		}
		return v, nil
	}

	mux.Handle("GET /v1/audit/diversity/crosswalk", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v, err := view(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "crosswalk read failed")
			return
		}
		writeJSON(w, http.StatusOK, v)
	})))

	mux.Handle("PUT /v1/audit/diversity/crosswalk", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body crosswalkBody
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		doc, _, err := body.document()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := bs.Put(r.Context(), crosswalkBlobPath, doc, blob.PutOptions{ContentType: "application/toml"}); err != nil {
			writeGrainWriteError(w, err)
			return
		}
		v, err := view(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "crosswalk read failed")
			return
		}
		writeJSON(w, http.StatusOK, v)
	})))

	mux.Handle("DELETE /v1/audit/diversity/crosswalk", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := bs.Delete(r.Context(), crosswalkBlobPath); err != nil && !errors.Is(err, blob.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, "crosswalk delete failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))

	mux.Handle("POST /v1/audit/diversity/preview", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		filters, err := auditFilters(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		var body crosswalkBody
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		doc, _, err := body.document()
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		cw, err := diversity.FromBytes(doc)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		sums, _, err := ix.SummariesWithGeneration(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, errScanFailed.Error())
			return
		}
		a := diversity.NewAuditor(cw)
		for i := range sums {
			s := &sums[i]
			if !includeInAudit(s, filters) {
				continue
			}
			a.AddWeighted(summaryRefs(s, vix, auditLangs), workWeight(s))
		}
		writeJSON(w, http.StatusOK, auditResponse{
			Input:  "work index (cataloging corpus: suppressed included, tombstoned excluded)",
			Scope:  filters.String(),
			Report: a.Report(),
		})
	})))
}

// includeInAudit is the audit's corpus predicate, shared by compute and
// preview.
func includeInAudit(s *ingest.WorkSummary, filters auditFilterSet) bool {
	return !s.Tombstoned && filters.match(s.Extras)
}
