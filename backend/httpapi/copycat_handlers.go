package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/copycat"
	"github.com/freeeve/libcatalog/backend/marcview"
)

// registerCopycat mounts the copy-cataloging surface (tasks/050): external
// target search (librarian), staged batches with match review and commit
// (librarian), and target configuration (admin).
func registerCopycat(mux *http.ServeMux, svc *copycat.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	admin := auth.Require(verifier, auth.RoleAdmin)

	mux.Handle("GET /v1/copycat/targets", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targets, err := svc.Targets(r.Context())
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"targets": targets})
	})))

	mux.Handle("POST /v1/copycat/targets", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var t copycat.Target
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&t); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if writeCopycatError(w, svc.PutTarget(r.Context(), t)) {
			return
		}
		writeJSON(w, http.StatusOK, t)
	})))

	mux.Handle("DELETE /v1/copycat/targets/{name}", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeCopycatError(w, svc.DeleteTarget(r.Context(), r.PathValue("name"))) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))

	mux.Handle("POST /v1/copycat/search", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query   string   `json:"query"`
			Targets []string `json:"targets"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		results, failures, err := svc.SearchAll(r.Context(), req.Query, req.Targets)
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"results": results, "failures": failures})
	})))

	mux.Handle("POST /v1/copycat/batches", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Label   string               `json:"label"`
			Source  string               `json:"source"`
			Records []marcview.RecordDoc `json:"records"`
			// MRC carries a base64 ISO 2709 upload instead of records.
			MRC string `json:"mrc"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		var batch copycat.Batch
		var records []copycat.StagedRecord
		var err error
		if req.MRC != "" {
			raw, decErr := base64.StdEncoding.DecodeString(req.MRC)
			if decErr != nil {
				writeError(w, http.StatusBadRequest, "mrc must be base64")
				return
			}
			batch, records, err = svc.StageMARC(r.Context(), req.Label, raw, id.Email)
		} else {
			source := req.Source
			if source == "" {
				source = "search"
			}
			batch, records, err = svc.Stage(r.Context(), req.Label, source, req.Records, id.Email)
		}
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"batch": batch, "records": records})
	})))

	mux.Handle("GET /v1/copycat/batches", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batches, err := svc.Batches(r.Context())
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"batches": batches})
	})))

	mux.Handle("GET /v1/copycat/batches/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		batch, records, err := svc.GetBatch(r.Context(), r.PathValue("id"))
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"batch": batch, "records": records})
	})))

	mux.Handle("POST /v1/copycat/batches/{id}/review", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Policy    string            `json:"policy"`
			Decisions map[string]string `json:"decisions"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		decisions := map[int]string{}
		for k, v := range req.Decisions {
			idx, err := strconv.Atoi(k)
			if err != nil {
				writeError(w, http.StatusBadRequest, "decision keys are record indexes")
				return
			}
			decisions[idx] = v
		}
		batch, err := svc.Review(r.Context(), r.PathValue("id"), req.Policy, decisions)
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, batch)
	})))

	mux.Handle("POST /v1/copycat/batches/{id}/commit", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		batch, err := svc.Commit(r.Context(), r.PathValue("id"), id.Email)
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, batch)
	})))

	mux.Handle("DELETE /v1/copycat/batches/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeCopycatError(w, svc.DeleteBatch(r.Context(), r.PathValue("id"))) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}

func writeCopycatError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, copycat.ErrValidation):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, copycat.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		writeError(w, http.StatusInternalServerError, "copy cataloging failed")
	}
	return true
}
