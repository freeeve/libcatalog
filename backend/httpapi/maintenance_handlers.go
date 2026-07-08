package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/workindex"
)

// registerMaintenance mounts the tasks/051 maintenance surfaces: the
// visibility stance (tombstone with optional redirect, suppress) and the
// duplicate-detection worklist over the shared work index.
func registerMaintenance(mux *http.ServeMux, bs blob.Store, ix *workindex.Index, queue *suggest.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	registerItemsBulk(mux, bs, ix, queue, librarian)

	mux.Handle("GET /v1/works/{id}/visibility", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grain, _, workID, ok := readWorkGrain(w, r, bs)
		if !ok {
			return
		}
		v, err := bibframe.Visibility(grain, workID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain parse failed")
			return
		}
		writeJSON(w, http.StatusOK, v)
	})))

	mux.Handle("POST /v1/works/{id}/visibility", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		var req struct {
			Action     string `json:"action"` // tombstone | untombstone | suppress | unsuppress
			RedirectTo string `json:"redirectTo"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if req.RedirectTo != "" && !workIDPattern.MatchString(req.RedirectTo) {
			writeError(w, http.StatusBadRequest, "bad redirect target")
			return
		}
		mutate := map[string]func([]byte) ([]byte, error){
			"tombstone":   func(g []byte) ([]byte, error) { return bibframe.SetTombstone(g, workID, req.RedirectTo) },
			"untombstone": func(g []byte) ([]byte, error) { return bibframe.ClearTombstone(g, workID) },
			"suppress":    func(g []byte) ([]byte, error) { return bibframe.SetSuppressed(g, workID, true) },
			"unsuppress":  func(g []byte) ([]byte, error) { return bibframe.SetSuppressed(g, workID, false) },
		}[req.Action]
		if mutate == nil {
			writeError(w, http.StatusBadRequest, "action must be tombstone|untombstone|suppress|unsuppress")
			return
		}
		etag, err := mutateWorkGrain(r, bs, ix, workID, mutate)
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "VISIBILITY_" + req.Action, Actor: id.Email, ETag: etag,
				Note: req.RedirectTo,
			})
		}
		grain, _, _ := bs.Get(r.Context(), bibframe.GrainPath(workID))
		v, _ := bibframe.Visibility(grain, workID)
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, v)
	})))

	// Holdings: the minimal bf:Item model (tasks/051), read per work and
	// replaced per instance.
	mux.Handle("GET /v1/works/{id}/items", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grain, etag, workID, ok := readWorkGrain(w, r, bs)
		if !ok {
			return
		}
		gi, err := identity.ScanGrain(grain)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "grain parse failed")
			return
		}
		items := map[string][]bibframe.Item{}
		for _, inst := range gi.Instances {
			list, err := bibframe.ItemsOf(grain, inst.InstanceID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "grain parse failed")
				return
			}
			items[inst.InstanceID] = list
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]any{"workId": workID, "etag": etag, "items": items})
	})))

	mux.Handle("PUT /v1/works/{id}/items", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		var req struct {
			InstanceID string          `json:"instanceId"`
			Items      []bibframe.Item `json:"items"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.InstanceID == "" {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if len(req.Items) > 200 {
			writeError(w, http.StatusBadRequest, "at most 200 items per instance")
			return
		}
		etag, err := mutateWorkGrain(r, bs, ix, workID, func(g []byte) ([]byte, error) {
			return bibframe.SetItems(g, req.InstanceID, req.Items)
		})
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "ITEMS_EDIT", Actor: id.Email, ETag: etag,
				Note: req.InstanceID,
			})
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]string{"workId": workID, "etag": etag})
	})))

	// The withdrawal review queue (tasks/078): feed-only works the last
	// reconciliation flagged as gone from their feed, awaiting a curator's
	// suppress-or-keep call. Auto-suppressed rows are decided, so they stay
	// out of the queue.
	mux.Handle("GET /v1/withdrawn", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		summaries, err := ix.Summaries(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		queue := []ingest.WorkSummary{}
		for _, s := range summaries {
			if s.Withdrawn != "" && !s.Suppressed {
				queue = append(queue, s)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"works": queue})
	})))

	mux.Handle("POST /v1/works/{id}/withdrawn", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		var req struct {
			Action string `json:"action"` // keep | suppress
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		mutate := map[string]func([]byte) ([]byte, error){
			// keep: the withdrawal is overruled -- clear the flag and record
			// the decision so reconciliation never re-flags this work.
			"keep": func(g []byte) ([]byte, error) {
				g, err := bibframe.ClearWithdrawn(g, workID)
				if err != nil {
					return nil, err
				}
				return bibframe.SetFeedKept(g, workID, true)
			},
			// suppress: hide from projection; the flag stays as the reason.
			"suppress": func(g []byte) ([]byte, error) { return bibframe.SetSuppressed(g, workID, true) },
		}[req.Action]
		if mutate == nil {
			writeError(w, http.StatusBadRequest, "action must be keep|suppress")
			return
		}
		etag, err := mutateWorkGrain(r, bs, ix, workID, mutate)
		if err != nil {
			writeMutateError(w, err)
			return
		}
		if queue != nil {
			queue.WriteAudit(r.Context(), suggest.AuditEntry{
				WorkID: workID, Action: "WITHDRAWN_" + req.Action, Actor: id.Email, ETag: etag,
			})
		}
		grain, _, _ := bs.Get(r.Context(), bibframe.GrainPath(workID))
		v, _ := bibframe.Visibility(grain, workID)
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, v)
	})))

	// The duplicate-detection worklist: Works sharing a clustering key
	// (author+title+language) that nonetheless hold separate ids -- the
	// candidates the merge tool resolves (tasks/051).
	mux.Handle("GET /v1/duplicates", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		type dupWork struct {
			WorkID string `json:"workId"`
			Title  string `json:"title,omitempty"`
		}
		type dupGroup struct {
			Key   string    `json:"key"`
			Works []dupWork `json:"works"`
		}
		byKey, err := ix.DuplicateGroups(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		titles := map[string]string{}
		if summaries, err := ix.Summaries(r.Context()); err == nil {
			for _, s := range summaries {
				titles[s.WorkID] = s.Title
			}
		}
		groups := []dupGroup{}
		for key, ids := range byKey {
			g := dupGroup{Key: key}
			for _, id := range ids {
				g.Works = append(g.Works, dupWork{WorkID: id, Title: titles[id]})
			}
			groups = append(groups, g)
		}
		sort.Slice(groups, func(i, j int) bool { return groups[i].Key < groups[j].Key })
		writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
	})))
}
