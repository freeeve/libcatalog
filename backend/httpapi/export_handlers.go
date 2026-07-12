package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strconv"
	"strings"

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
// the selection matched.
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
		data, gzipped, err := svc.OpenStored(r.Context(), job)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "output unavailable")
			return
		}
		writeExport(w, r, job, data, gzipped)
	})
}

// writeExport serves a stored export, honouring the shape it was stored in
// . Exports are gzipped at rest, and the format decides whether the
// client is told so:
//
//   - a .gz artifact (nquads, jsonld, marc) goes out as application/gzip, bytes
//     untouched -- the librarian keeps a 2GB dump at ~100MB;
//   - CSV goes out as text/csv with Content-Encoding: gzip, so the browser saves
//     an ordinary .csv and Excel opens it.
//
// A client that does not accept gzip gets CSV decompressed here. That branch is
// why the response varies on Accept-Encoding, and it is not hypothetical: curl
// sends no Accept-Encoding by default.
//
// Jobs written before outputs were gzipped hold plain bytes; they are served
// as they always were rather than being compressed on the way out.
func writeExport(w http.ResponseWriter, r *http.Request, job export.Job, data []byte, gzipped bool) {
	w.Header().Set("Content-Disposition", `attachment; filename="`+path.Base(job.OutputPath)+`"`)
	if !gzipped {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
		return
	}
	del := export.DeliveryFor(job.ID, job.Format)
	if del.ContentEncoding != "gzip" {
		w.Header().Set("Content-Type", del.ContentType)
		_, _ = w.Write(data)
		return
	}
	w.Header().Set("Content-Type", del.ContentType)
	w.Header().Add("Vary", "Accept-Encoding")
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(data)
		return
	}
	plain, err := export.Gunzip(data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "output unavailable")
		return
	}
	_, _ = w.Write(plain)
}

// acceptsGzip reports whether an Accept-Encoding header permits gzip. An
// explicit q=0 is a refusal, not a preference, so "gzip;q=0" must not match a
// bare substring search.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		name := strings.ToLower(strings.TrimSpace(fields[0]))
		if name != "gzip" && name != "*" {
			continue
		}
		for _, p := range fields[1:] {
			if q, ok := strings.CutPrefix(strings.ToLower(strings.TrimSpace(p)), "q="); ok {
				if v, err := strconv.ParseFloat(q, 64); err == nil && v == 0 {
					return false
				}
			}
		}
		return true
	}
	return false
}
