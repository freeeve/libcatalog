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
// mapping writeMutateError gives the write half, tasks/115/116). Callers use
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
