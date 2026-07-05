package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadOnlyGuard(t *testing.T) {
	reached := false
	guarded := readOnlyGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		method, path string
		wantStatus   int
		wantReached  bool
	}{
		// Reads always pass.
		{"GET", "/v1/works", http.StatusOK, true},
		{"GET", "/v1/profiles/work-monograph", http.StatusOK, true},
		// Auth must work so a visitor can sign in / refresh.
		{"POST", "/v1/auth/login", http.StatusOK, true},
		{"POST", "/v1/auth/refresh", http.StatusOK, true},
		// Non-persisting POSTs pass.
		{"POST", "/v1/copycat/search", http.StatusOK, true},
		{"POST", "/v1/batch/resolve", http.StatusOK, true},
		// Dry-run-capable editor endpoints pass (execute is blocked at blob).
		{"POST", "/v1/works/w123/ops", http.StatusOK, true},
		{"POST", "/v1/works/w123/marc", http.StatusOK, true},
		{"POST", "/v1/batch/ops", http.StatusOK, true},
		// Other non-persisting editor reads pass.
		{"POST", "/v1/works/w123/marc/preview", http.StatusOK, true},
		{"POST", "/v1/works/w123/validate", http.StatusOK, true},
		{"POST", "/v1/works/w123/subjects/lookup", http.StatusOK, true},
		// Editorial / config writes are rejected.
		{"PUT", "/v1/works/w123", http.StatusForbidden, false},
		{"POST", "/v1/review", http.StatusForbidden, false},
		{"POST", "/v1/publish", http.StatusForbidden, false},
		{"PUT", "/v1/profiles/work-monograph", http.StatusForbidden, false},
		{"POST", "/v1/copycat/targets", http.StatusForbidden, false},
		{"DELETE", "/v1/copycat/batches/b1", http.StatusForbidden, false},
		{"POST", "/v1/terms", http.StatusForbidden, false},
	}
	for _, c := range cases {
		reached = false
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != c.wantStatus {
			t.Errorf("%s %s: status = %d, want %d", c.method, c.path, rec.Code, c.wantStatus)
		}
		if reached != c.wantReached {
			t.Errorf("%s %s: reached inner = %v, want %v", c.method, c.path, reached, c.wantReached)
		}
	}
}
