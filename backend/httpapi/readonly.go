package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/freeeve/libcat/storage/blob"
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
//
// The allowlisted execute paths reach the store rather than this guard, so they
// answer through writeGrainWriteError / writeMutateError, which map
// blob.ErrReadOnly onto the same 403 and the same wording. A client cannot tell
// which layer refused it, which is the point: those two routes
// answered 500 "grain write failed".
func readOnlyGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isMutatingMethod(r.Method) && !readOnlyAllowed(r.URL.Path) {
			writeReadOnly(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// readOnlyNotice is what a client is told when a write is refused because the
// deployment does not accept writes. The guard and the store's own refusal must
// be indistinguishable: the allowlisted execute paths reach the store instead of
// the guard, and used to answer 500 "grain write failed" -- a claim the server
// is broken, on an ordinary user action, in the mode strangers touch.
const readOnlyNotice = "read-only demo: changes are not saved"

func writeReadOnly(w http.ResponseWriter) {
	writeError(w, http.StatusForbidden, readOnlyNotice)
}

// writeGrainWriteError maps a grain Put failure onto its status. A read-only
// store is a deployment policy (403), not a fault (500); blob.ErrReadOnly is
// exported for exactly this and was consulted nowhere.
func writeGrainWriteError(w http.ResponseWriter, err error) {
	if errors.Is(err, blob.ErrReadOnly) {
		writeReadOnly(w)
		return
	}
	writeError(w, http.StatusInternalServerError, "grain write failed")
}

func isMutatingMethod(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// workScopedAllowed are the per-work editor routes that never persist in
// read-only mode: /v1/works/{id}/<suffix>. Matching the shape of the route
// rather than the tail of the path keeps a future /v1/queue/ops or
// /v1/exports/marc out of the allowlist. The old suffix match let
// any route ending in these words through, relying entirely on the blob store
// to catch it -- and a route writing to the *document* store would not have been
// caught at all, since only the blob is wrapped read-only.
var workScopedAllowed = map[string]bool{
	"ops": true, "marc": true, "marc/preview": true, "validate": true, "subjects/lookup": true,
}

// readOnlyAllowed reports whether a mutating request to path is permitted in
// read-only mode (see readOnlyGuard for the rationale per entry).
func readOnlyAllowed(path string) bool {
	if strings.HasPrefix(path, "/v1/auth/") {
		return true
	}
	switch path {
	case "/v1/copycat/search", "/v1/batch/resolve", "/v1/batch/ops":
		return true
	}
	rest, ok := strings.CutPrefix(path, "/v1/works/")
	if !ok {
		return false
	}
	// The id itself is not validated here: a malformed one should reach the
	// handler and earn its 400, not be masked by the guard's 403.
	workID, suffix, ok := strings.Cut(rest, "/")
	if !ok || workID == "" {
		return false
	}
	return workScopedAllowed[suffix]
}
