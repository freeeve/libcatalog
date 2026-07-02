package httpapi

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/freeeve/libcatalog/ingest"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/auth"
)

// worksList serves the SPA's work search: WorkSummaries scanned from the
// grain tree, cached briefly (the editing UI needs freshness after a
// publish, not per-keystroke scans).
type worksList struct {
	bs  blob.Store
	mu  sync.Mutex
	at  time.Time
	all []ingest.WorkSummary
}

const worksListTTL = 30 * time.Second

func (wl *worksList) summaries(r *http.Request) ([]ingest.WorkSummary, error) {
	wl.mu.Lock()
	defer wl.mu.Unlock()
	if time.Since(wl.at) < worksListTTL && wl.all != nil {
		return wl.all, nil
	}
	summaries, _, err := ingest.ScanSummaries(r.Context(), wl.bs, "data/works/")
	if err != nil {
		return nil, err
	}
	wl.all = summaries
	wl.at = time.Now()
	return summaries, nil
}

// registerWorksList mounts GET /v1/works?q=&limit= (librarian).
func registerWorksList(mux *http.ServeMux, bs blob.Store, verifier auth.TokenVerifier) {
	wl := &worksList{bs: bs}
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	mux.Handle("GET /v1/works", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		all, err := wl.summaries(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 500 {
				limit = n
			}
		}
		matches := make([]ingest.WorkSummary, 0, limit)
		for _, s := range all {
			if q != "" && !summaryMatches(s, q) {
				continue
			}
			matches = append(matches, s)
			if len(matches) >= limit {
				break
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"works": matches, "total": len(all)})
	})))
}

func summaryMatches(s ingest.WorkSummary, q string) bool {
	if strings.Contains(strings.ToLower(s.Title), q) || strings.Contains(strings.ToLower(s.WorkID), q) {
		return true
	}
	for _, c := range s.Contributors {
		if strings.Contains(strings.ToLower(c), q) {
			return true
		}
	}
	for _, tag := range s.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return true
		}
	}
	for _, isbn := range s.ISBNs {
		if strings.Contains(isbn, q) {
			return true
		}
	}
	return false
}
