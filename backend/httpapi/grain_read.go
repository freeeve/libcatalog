package httpapi

import (
	"errors"
	"net/http"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"
)

// readWorkGrain is the shared read half of every per-work handler: validate
// the {id} path value, read its grain, and write the error response itself
// on failure -- bad id 400, missing work 404, store fault 500 (the same
// mapping writeMutateError gives the write half). Callers use
// the returns only when ok is true.
func readWorkGrain(w http.ResponseWriter, r *http.Request, bs blob.Store) (grain []byte, etag, workID string, ok bool) {
	workID = r.PathValue("id")
	if !workIDPattern.MatchString(workID) {
		writeError(w, http.StatusBadRequest, "bad work id")
		return nil, "", "", false
	}
	grain, etag, err := bs.Get(r.Context(), bibframe.GrainPath(workID))
	if errors.Is(err, blob.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such work")
		return nil, "", "", false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "grain read failed")
		return nil, "", "", false
	}
	return grain, etag, workID, true
}

// writeGrainConflict is the answer every client-token PUT owes a stale token:
// 412 carrying the grain as it now stands, so the client can show the edit that
// beat it and rebase deliberately rather than clobber. Shared so the two callers
// cannot drift into answering the same condition differently.
func writeGrainConflict(w http.ResponseWriter, workID, etag string, grain []byte) {
	w.Header().Set("ETag", etag)
	writeJSON(w, http.StatusPreconditionFailed, grainView{WorkID: workID, ETag: etag, NQuads: string(grain)})
}

// requireIfMatch reads the client's precondition, refusing a request that has
// none. A read-modify-write cycle without a token cannot be checked for a lost
// update, so the token is not optional -- it is the whole mechanism.
func requireIfMatch(w http.ResponseWriter, r *http.Request) (string, bool) {
	ifMatch := r.Header.Get("If-Match")
	if ifMatch == "" {
		writeError(w, http.StatusPreconditionRequired, "If-Match required")
		return "", false
	}
	return ifMatch, true
}
