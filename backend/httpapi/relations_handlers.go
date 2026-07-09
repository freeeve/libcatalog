package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

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

// relationEntry is one linked work with its display title resolved.
type relationEntry struct {
	WorkID string `json:"workId"`
	Title  string `json:"title,omitempty"`
}

// registerRelations mounts the work-to-work relationship surface
// (tasks/221, 058 item 3): GET lists a work's editorial hasPart/partOf
// links with titles; POST adds and DELETE removes a link, writing both
// directions.
func registerRelations(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

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
		// Both grains must exist before either side is written, so a typo'd
		// target never leaves a half-link.
		for _, wid := range []string{workID, req.Target} {
			if _, _, err := bs.Get(r.Context(), bibframe.GrainPath(wid)); err != nil {
				writeError(w, http.StatusNotFound, "no such work: "+wid)
				return
			}
		}
		// An add must not close a containment cycle, checked before either
		// write for the same reason the existence check is: no half-link.
		if add {
			whole, part := workID, req.Target
			if kind.pred == bibframe.PredPartOf {
				whole, part = req.Target, workID
			}
			cycle, err := containmentCycle(r.Context(), bs, whole, part)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "grain store unavailable")
				return
			}
			if cycle {
				writeError(w, http.StatusBadRequest, "would create a containment cycle: "+part+" already contains "+whole)
				return
			}
		}
		if _, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
			return bibframe.SetWorkRelation(g, workID, kind.pred, req.Target, add)
		}); err != nil {
			writeMutateError(w, err)
			return
		}
		if _, err := mutateWorkGrain(r, bs, ix, req.Target, func(g []byte) ([]byte, error) {
			return bibframe.SetWorkRelation(g, req.Target, kind.inverse, workID, add)
		}); err != nil {
			// The forward statement is applied; report the asymmetry rather
			// than hide it. Retrying the same call converges (adds are
			// idempotent, removes of absent quads are no-ops).
			writeError(w, http.StatusInternalServerError, "link applied on "+workID+" but the inverse on "+req.Target+" failed; retry to converge")
			return
		}
		action := "WORK_RELATE"
		if !add {
			action = "WORK_UNRELATE"
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: action, Actor: id.Email, Note: req.Kind + " " + req.Target,
			})
		}
		w.WriteHeader(http.StatusNoContent)
	}
	mux.Handle("POST /v1/works/{id}/relations", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutate(w, r, true)
	})))
	mux.Handle("DELETE /v1/works/{id}/relations", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutate(w, r, false)
	})))
}

// relationWalkLimit bounds the containmentCycle walk. A containment tree a
// cataloger builds by hand -- a set, its volumes, their parts -- is orders of
// magnitude smaller; the cap only stops a pathological corpus from turning
// one link into an unbounded read.
const relationWalkLimit = 512

// containmentCycle reports whether asserting "whole contains part" would
// close a cycle, by walking bf:hasPart edges down from part looking for
// whole (tasks/232). Every link writes its inverse on the target, so the
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
