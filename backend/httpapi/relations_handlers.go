package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/workindex"
)

// relationKinds maps the API's relation kind to its predicate and the
// inverse written on the target work: A hasPart B stores partOf A on B, so
// both grains self-describe.
var relationKinds = map[string]struct{ pred, inverse string }{
	"hasPart": {bibframe.PredHasPart, bibframe.PredPartOf},
	"partOf":  {bibframe.PredPartOf, bibframe.PredHasPart},
}

// errRelationCycle travels out of the grain-mutation closure, which can only
// return an error, and is mapped back to the guard's 400.
var errRelationCycle = errors.New("would create a containment cycle")

func cycleMessage(whole, part string) string {
	return "would create a containment cycle: " + part + " already contains " + whole
}

// relationEntry is one linked work with its display title resolved.
type relationEntry struct {
	WorkID string `json:"workId"`
	Title  string `json:"title,omitempty"`
}

// registerRelations mounts the work-to-work relationship surface
// : GET lists a work's editorial hasPart/partOf
// links with titles; POST adds and DELETE removes a link, writing both
// directions.
func registerRelations(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, verifier auth.TokenVerifier, logger *slog.Logger) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	// relationMu serializes the whole check-then-write span of a relation
	// mutation.
	//
	// containmentCycle is a time-of-check test over committed grains, and the
	// two writes that follow land on two different grains. mutateWorkGrain's
	// compare-and-swap has no shared object to arbitrate on, so two adds fired
	// in opposite directions each saw a graph without the other's edge, both
	// passed the guard, and both wrote their forward statement: the cycle the
	// guard exists to prevent. This is the barcode race with a different
	// invariant.
	//
	// Relation edits are rare and cataloger-paced, so one lock costs nothing.
	// It is correct for a single process, which is the only deployment libcat
	// supports today; the seam to replace when that changes is a reservation in
	// the shared store, as it is for barcodes.
	var relationMu sync.Mutex

	mux.Handle("GET /v1/works/{id}/relations", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		grain, _, err := bs.Get(r.Context(), bibframe.GrainPath(workID))
		if errors.Is(err, blob.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such work")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain store unavailable")
			return
		}
		rel, err := bibframe.WorkRelationsOf(grain, workID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "unreadable grain")
			return
		}
		titles := workTitles(r, ix)
		entries := func(ids []string) []relationEntry {
			out := make([]relationEntry, 0, len(ids))
			for _, id := range ids {
				out = append(out, relationEntry{WorkID: id, Title: titles[id]})
			}
			return out
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"hasPart": entries(rel.HasPart), "partOf": entries(rel.PartOf),
		})
	})))

	mutate := func(w http.ResponseWriter, r *http.Request, add bool) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		var req struct {
			Kind   string `json:"kind"`
			Target string `json:"target"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad JSON")
			return
		}
		kind, ok := relationKinds[req.Kind]
		if !ok {
			writeError(w, http.StatusBadRequest, "kind must be hasPart or partOf")
			return
		}
		if !workIDPattern.MatchString(workID) || !workIDPattern.MatchString(req.Target) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		if req.Target == workID {
			writeError(w, http.StatusBadRequest, "a work cannot relate to itself")
			return
		}
		// Everything from here to the second write is one critical section: the
		// cycle guard reads committed grains, and its answer must still be true
		// when the writes land.
		relationMu.Lock()
		defer relationMu.Unlock()
		// Both grains must exist before either side is written, so a typo'd
		// target never leaves a half-link.
		for _, wid := range []string{workID, req.Target} {
			if _, _, err := bs.Get(r.Context(), bibframe.GrainPath(wid)); err != nil {
				writeError(w, http.StatusNotFound, "no such work: "+wid)
				return
			}
		}
		// An add must not close a containment cycle, checked before either
		// write for the same reason the existence check is: no half-link. The
		// answer is re-established inside the forward write below, which is
		// what actually makes it binding.
		whole, part := workID, req.Target
		if kind.pred == bibframe.PredPartOf {
			whole, part = req.Target, workID
		}
		if add {
			cycle, err := containmentCycle(r.Context(), bs, whole, part)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "grain store unavailable")
				return
			}
			if cycle {
				writeError(w, http.StatusBadRequest, cycleMessage(whole, part))
				return
			}
		}
		if _, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
			// Re-check on every attempt. relationMu keeps other relation edits
			// out, but a plain grain write -- PUT /v1/works/{id}, a batch patch --
			// can add a hasPart quad and lose us the compare-and-swap. The retry
			// re-runs this closure precisely because the graph moved, and the
			// guard's earlier answer was about a graph that no longer exists.
			if add {
				cycle, err := containmentCycle(r.Context(), bs, whole, part)
				if err != nil {
					return nil, errGrainStore
				}
				if cycle {
					return nil, errRelationCycle
				}
			}
			return bibframe.SetWorkRelation(g, workID, kind.pred, req.Target, add)
		}); err != nil {
			if errors.Is(err, errRelationCycle) {
				writeError(w, http.StatusBadRequest, cycleMessage(whole, part))
				return
			}
			writeMutateError(w, err)
			return
		}
		if _, err := mutateWorkGrain(r, bs, ix, req.Target, func(g []byte) ([]byte, error) {
			return bibframe.SetWorkRelation(g, req.Target, kind.inverse, workID, add)
		}); err != nil {
			// The forward statement is applied and its inverse is not. Undo it
			// rather than report a half-link and prescribe a retry: for an add,
			// the surviving forward edge is itself a containment claim, and a
			// retry would be refused by the cycle guard reading the edge this
			// very request wrote. Compensating leaves nothing
			// applied, so "retry" is true again.
			if _, cerr := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
				return bibframe.SetWorkRelation(g, workID, kind.pred, req.Target, !add)
			}); cerr != nil {
				logger.Error("relation rollback failed: one work asserts a link the other does not",
					"workId", workID, "target", req.Target, "kind", req.Kind, "add", add,
					"actor", id.Email, "inverse", err, "rollback", cerr)
				// The record changed, so the change is attributable.
				writeRelationAudit(r, queue, id.Email, workID, req.Kind, req.Target, add, true)
				writeError(w, http.StatusInternalServerError,
					"the link on "+workID+" was written, its inverse on "+req.Target+" failed, and "+workID+" could not be rolled back: delete the link from both records, then re-add it once")
				return
			}
			logger.Error("relation inverse write failed; the forward link was rolled back",
				"workId", workID, "target", req.Target, "kind", req.Kind, "add", add,
				"actor", id.Email, "err", err)
			writeError(w, http.StatusInternalServerError,
				"the inverse link on "+req.Target+" could not be written; nothing was applied, retry")
			return
		}
		writeRelationAudit(r, queue, id.Email, workID, req.Kind, req.Target, add, false)
		w.WriteHeader(http.StatusNoContent)
	}
	mux.Handle("POST /v1/works/{id}/relations", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutate(w, r, true)
	})))
	mux.Handle("DELETE /v1/works/{id}/relations", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutate(w, r, false)
	})))
}

// writeRelationAudit records a relation change. stranded marks the one case
// where the two grains disagree: the forward statement was written, its inverse
// failed, and the rollback failed too. That record changed, so it is in the
// history -- otherwise the single work needing repair is the one nothing names
// . A compensated failure changed nothing and is not audited.
func writeRelationAudit(r *http.Request, queue *suggest.Service, actor, workID, kind, target string, add, stranded bool) {
	if queue == nil {
		return
	}
	action := "WORK_RELATE"
	if !add {
		action = "WORK_UNRELATE"
	}
	note := kind + " " + target
	if stranded {
		note += " (inverse missing on " + target + "; delete from both records and re-add)"
	}
	queue.WriteAudit(r.Context(), suggest.AuditEntry{
		WorkID: workID, Action: action, Actor: actor, Note: note,
	})
}

// relationWalkLimit bounds the containmentCycle walk. A containment tree a
// cataloger builds by hand -- a set, its volumes, their parts -- is orders of
// magnitude smaller; the cap only stops a pathological corpus from turning
// one link into an unbounded read.
const relationWalkLimit = 512

// containmentCycle reports whether asserting "whole contains part" would
// close a cycle, by walking bf:hasPart edges down from part looking for
// whole. Every link writes its inverse on the target, so the
// whole containment graph is readable off hasPart edges alone -- A partOf B
// is stored as B hasPart A -- and the two-work contradiction (A hasPart B
// plus A partOf B) is simply the depth-1 case of the same walk. A target
// whose grain has vanished ends its branch rather than failing the write:
// a dangling link is not a cycle.
func containmentCycle(ctx context.Context, bs blob.Store, whole, part string) (bool, error) {
	seen := map[string]bool{part: true}
	for queue := []string{part}; len(queue) > 0 && len(seen) <= relationWalkLimit; {
		cur := queue[0]
		queue = queue[1:]
		grain, _, err := bs.Get(ctx, bibframe.GrainPath(cur))
		if errors.Is(err, blob.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		rel, err := bibframe.WorkRelationsOf(grain, cur)
		if err != nil {
			return false, err
		}
		for _, child := range rel.HasPart {
			if child == whole {
				return true, nil
			}
			if !seen[child] {
				seen[child] = true
				queue = append(queue, child)
			}
		}
	}
	return false, nil
}

// workTitles maps work id -> title from the shared index, best-effort: a
// missing index degrades to id-only display, not an error.
func workTitles(r *http.Request, ix *workindex.Index) map[string]string {
	titles := map[string]string{}
	summaries, err := ix.Summaries(r.Context())
	if err != nil {
		return titles
	}
	for _, s := range summaries {
		titles[s.WorkID] = s.Title
	}
	return titles
}
