package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"

	"github.com/freeeve/libcatalog/bibframe"
	"github.com/freeeve/libcatalog/identity"
	"github.com/freeeve/libcatalog/ingest"
	"github.com/freeeve/libcatalog/storage/blob"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/suggest"
)

// registerMaintenance mounts the tasks/051 maintenance surfaces: the
// visibility stance (tombstone with optional redirect, suppress) and the
// duplicate-detection worklist over the identity scan.
func registerMaintenance(mux *http.ServeMux, bs blob.Store, queue *suggest.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	mux.Handle("GET /v1/works/{id}/visibility", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		grain, _, err := bs.Get(r.Context(), bibframe.GrainPath(workID))
		if err != nil {
			writeError(w, http.StatusNotFound, "no such work")
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
		etag, err := mutateWorkGrain(r, bs, workID, mutate)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
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
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		grain, etag, err := bs.Get(r.Context(), bibframe.GrainPath(workID))
		if err != nil {
			writeError(w, http.StatusNotFound, "no such work")
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
		etag, err := mutateWorkGrain(r, bs, workID, func(g []byte) ([]byte, error) {
			return bibframe.SetItems(g, req.InstanceID, req.Items)
		})
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
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
		prior, _, err := bibframe.LoadPriorStore(r.Context(), bs, "data/works/", "")
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		titles := map[string]string{}
		if summaries, _, err := ingest.ScanSummaries(r.Context(), bs, "data/works/"); err == nil {
			for _, s := range summaries {
				titles[s.WorkID] = s.Title
			}
		}
		byKey := map[string]map[string]bool{}
		for _, gi := range prior.Grains {
			for _, wk := range gi.Works {
				if wk.ClusterKey == "" {
					continue
				}
				set := byKey[wk.ClusterKey]
				if set == nil {
					set = map[string]bool{}
					byKey[wk.ClusterKey] = set
				}
				set[wk.WorkID] = true
			}
		}
		groups := []dupGroup{}
		for key, set := range byKey {
			if len(set) < 2 {
				continue
			}
			g := dupGroup{Key: key}
			for id := range set {
				g.Works = append(g.Works, dupWork{WorkID: id, Title: titles[id]})
			}
			sort.Slice(g.Works, func(i, j int) bool { return g.Works[i].WorkID < g.Works[j].WorkID })
			groups = append(groups, g)
		}
		sort.Slice(groups, func(i, j int) bool { return groups[i].Key < groups[j].Key })
		writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
	})))
}
