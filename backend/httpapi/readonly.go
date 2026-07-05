package httpapi

import (
	"net/http"
	"strings"
)

// readOnlyGuard rejects mutating requests in deployment read-only (demo) mode,
// except a short allowlist that never persists editorial or config state:
//
//   - /v1/auth/* -- a visitor must be able to sign in and refresh.
//   - the non-persisting POSTs (external copy-cataloging search, batch selection
//     preview).
//   - the dry-run-capable editor endpoints (.../ops, .../marc, /v1/batch/ops):
//     their preview path writes nothing, and their execute path is separately
//     blocked at the read-only blob store, so a cataloger can still see diffs.
//
// Everything else that mutates -- record/authority writes, review, publish,
// term governance, copycat staging/commit, profile edits, drafts, macros,
// merges, withdrawals -- returns 403. Grain and blob-backed config writes are
// double-covered by the read-only blob store; this guard adds clean 403s and
// blocks the editorial writes that live in the document store.
func readOnlyGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMutatingMethod(r.Method) && !readOnlyAllowed(r.URL.Path) {
			writeError(w, http.StatusForbidden, "read-only demo: changes are not saved")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isMutatingMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// readOnlyAllowed reports whether a mutating request to path is permitted in
// read-only mode (see readOnlyGuard for the rationale per entry).
func readOnlyAllowed(path string) bool {
	if strings.HasPrefix(path, "/v1/auth/") {
		return true
	}
	switch path {
	case "/v1/copycat/search", "/v1/batch/resolve":
		return true
	}
	// Non-persisting POSTs the editor makes: dry-run-capable ops/marc (the
	// execute path is blocked at the blob store), the MARC preview, record
	// validation, and subject reconciliation against external targets. None of
	// these write.
	for _, suffix := range []string{"/ops", "/marc", "/marc/preview", "/validate", "/subjects/lookup"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}
