package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/batch"
	"github.com/freeeve/libcat/backend/export"
)

// exportView augments a job with its download link when ready.
type exportView struct {
	export.Job
	DownloadURL string `json:"downloadUrl,omitempty"`
}

// registerExports mounts the export-job surface (librarian; admins see all
// jobs). The download route is token-authenticated, not bearer-gated, so
// links paste into a browser. With the batch service present, a request may
// carry a batch.Selection (search / saved query / ids / all) that compiles
// to the id list at create time -- the export snapshots exactly the works
// the selection matched (tasks/048).
func registerExports(mux *http.ServeMux, svc *export.Service, batchSvc *batch.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	view := func(r *http.Request, job export.Job) exportView {
		v := exportView{Job: job}
		if job.Status == export.StatusDone {
			if url, err := svc.DownloadURL(r.Context(), job); err == nil {
				v.DownloadURL = url
			}
		}
		return v
	}

	mux.Handle("POST /v1/exports", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Format         export.Format              `json:"format"`
			Selection      export.Selection           `json:"selection"`
			BatchSelection *batch.Selection           `json:"batchSelection"`
			Authorities    *export.AuthoritySelection `json:"authorities"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if req.Authorities != nil {
			job, err := svc.CreateAuthorities(r.Context(), id.Email, req.Format, *req.Authorities)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusAccepted, view(r, job))
			return
		}
		if req.BatchSelection != nil {
			sel, ok := compileBatchSelection(w, r, batchSvc, *req.BatchSelection, id.Email)
			if !ok {
				return
			}
			req.Selection = sel
		}
		for _, workID := range req.Selection.WorkIDs {
			if !workIDPattern.MatchString(workID) {
				writeError(w, http.StatusBadRequest, "bad work id in selection")
				return
			}
		}
		job, err := svc.Create(r.Context(), id.Email, req.Format, req.Selection)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, view(r, job))
	})))

	mux.Handle("GET /v1/exports", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		jobs, err := svc.List(r.Context(), id.Email)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list failed")
			return
		}
		views := make([]exportView, 0, len(jobs))
		for _, job := range jobs {
			views = append(views, view(r, job))
		}
		writeJSON(w, http.StatusOK, map[string]any{"exports": views})
	})))

	mux.Handle("GET /v1/exports/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		job, err := svc.Get(r.Context(), id.Email, r.PathValue("id"), id.CanAdmin())
		if errors.Is(err, export.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such export")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		writeJSON(w, http.StatusOK, view(r, job))
	})))

	registerExportDownload(mux, svc)
}

// compileBatchSelection resolves a batch selection to the export's frozen id
// list ("all" passes through -- the runner scans the tree itself). Writes the
// error response and reports ok=false on failure.
func compileBatchSelection(w http.ResponseWriter, r *http.Request, batchSvc *batch.Service, sel batch.Selection, owner string) (export.Selection, bool) {
	if sel.Kind == batch.KindAll {
		return export.Selection{All: true}, true
	}
	if batchSvc == nil {
		writeError(w, http.StatusBadRequest, "batch selections are not enabled on this deployment")
		return export.Selection{}, false
	}
	targets, err := batchSvc.Resolve(r.Context(), sel, owner)
	if writeBatchError(w, err) {
		return export.Selection{}, false
	}
	if len(targets) == 0 {
		writeError(w, http.StatusBadRequest, "the selection matches no works")
		return export.Selection{}, false
	}
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		ids = append(ids, t.WorkID)
	}
	return export.Selection{WorkIDs: ids}, true
}

func registerExportDownload(mux *http.ServeMux, svc *export.Service) {
	mux.HandleFunc("GET /v1/exports/{id}/download", func(w http.ResponseWriter, r *http.Request) {
		// Admin=true read: the token, not the requester, authorizes here.
		job, err := svc.Get(r.Context(), "", r.PathValue("id"), true)
		if err != nil {
			writeError(w, http.StatusNotFound, "no such export")
			return
		}
		if !svc.VerifyToken(job, r.URL.Query().Get("token")) {
			writeError(w, http.StatusForbidden, "bad or expired token")
			return
		}
		data, err := svc.Open(r.Context(), job)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "output unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", `attachment; filename="`+job.ID+"."+string(job.Format)+`"`)
		_, _ = w.Write(data)
	})
}
