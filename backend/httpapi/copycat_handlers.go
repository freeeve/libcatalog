package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/copycat"
	"github.com/freeeve/libcat/backend/marcview"
	"github.com/freeeve/libcat/backend/suggest"
)

// registerCopycat mounts the copy-cataloging surface: external
// target search (librarian), staged batches with match review and commit
// (librarian), and target configuration (admin). queue, when set, receives an
// audit entry per target-configuration change -- a target decides which server
// records are copied from, so who repointed or removed one must be legible
// .
func registerCopycat(mux *http.ServeMux, svc *copycat.Service, verifier auth.TokenVerifier, queue *suggest.Service) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	admin := auth.Require(verifier, auth.RoleAdmin)
	// audit names the acting admin and the configuration change, mirroring the
	// user surface (auth_handlers.go). No WorkID: config entries partition by
	// month, not by work.
	audit := func(r *http.Request, action, note string) {
		if queue == nil {
			return
		}
		actor := ""
		if id, ok := auth.FromContext(r.Context()); ok {
			actor = id.Email
		}
		queue.WriteAudit(r.Context(), suggest.AuditEntry{Action: action, Actor: actor, Note: note})
	}

	mux.Handle("GET /v1/copycat/targets", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targets, err := svc.Targets(r.Context())
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"targets": targets})
	})))

	// The one-click preset row: the curated open sources, served from the same Go
	// table the seeder derives from so the UI cannot keep a copy that drifts
	//. Blurbs are added UI-side, keyed by name.
	mux.Handle("GET /v1/copycat/targets/suggested", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"targets": copycat.SuggestedTargets})
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
		// PutTarget is an upsert with CondNone, so this covers repointing a
		// seeded target at a new host -- the silent overwrite the audit answers.
		audit(r, "COPYCAT_TARGET_SET", fmt.Sprintf("%s -> %s (%s)", t.Name, t.URL, t.Protocol))
		writeJSON(w, http.StatusOK, t)
	})))

	mux.Handle("DELETE /v1/copycat/targets/{name}", admin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")
		if writeCopycatError(w, svc.DeleteTarget(r.Context(), name)) {
			return
		}
		audit(r, "COPYCAT_TARGET_DELETE", name)
		w.WriteHeader(http.StatusNoContent)
	})))

	mux.Handle("POST /v1/copycat/search", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query   string              `json:"query"`
			Fields  []copycat.FieldTerm `json:"fields"`
			Targets []string            `json:"targets"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		results, failures, warnings, err := svc.SearchAll(r.Context(), req.Query, req.Fields, req.Targets)
		if writeCopycatError(w, err) {
			return
		}
		// warnings are the targets that answered, incompletely. Their hits are in
		// results; a client that renders only failures would tell a cataloger the
		// short set is the whole set.
		writeJSON(w, http.StatusOK, map[string]any{"results": results, "failures": failures, "warnings": warnings})
	})))

	// Original cataloging: blank-record skeletons, and staging a
	// record born in the editor rather than fetched from a target.
	mux.Handle("GET /v1/copycat/templates", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		templates, err := copycat.LoadTemplates()
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
	})))

	mux.Handle("POST /v1/copycat/original", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Label  string             `json:"label"`
			Record marcview.RecordDoc `json:"record"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		batch, records, fieldErrs, err := svc.StageOriginal(r.Context(), req.Label, req.Record, id.Email)
		if len(fieldErrs) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":  "the record fails minimum viability",
				"fields": fieldErrs,
			})
			return
		}
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"batch": batch, "records": records})
	})))

	mux.Handle("POST /v1/copycat/batches", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Label   string               `json:"label"`
			Source  string               `json:"source"`
			Records []marcview.RecordDoc `json:"records"`
			// MRC carries a base64 ISO 2709 upload instead of records.
			MRC string `json:"mrc"`
			// Policy pre-sets the overlay policy (a staging profile's
			// choice); empty keeps the replace-feed default.
			Policy string `json:"policy"`
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
		if req.Policy != "" {
			batch, err = svc.Review(r.Context(), batch.ID, req.Policy, nil)
			if writeCopycatError(w, err) {
				return
			}
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

	// Revert: roll a committed batch back grain by grain;
	// post-commit editorial edits survive as reported skips.
	mux.Handle("POST /v1/copycat/batches/{id}/revert", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		result, err := svc.Revert(r.Context(), r.PathValue("id"), id.Email)
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))

	// Staging profiles: saved import configurations.
	mux.Handle("GET /v1/copycat/profiles", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		profiles, err := svc.Profiles(r.Context())
		if writeCopycatError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
	})))

	mux.Handle("POST /v1/copycat/profiles", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p copycat.Profile
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&p); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if writeCopycatError(w, svc.PutProfile(r.Context(), p)) {
			return
		}
		writeJSON(w, http.StatusOK, p)
	})))

	mux.Handle("DELETE /v1/copycat/profiles/{name}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if writeCopycatError(w, svc.DeleteProfile(r.Context(), r.PathValue("name"))) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
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
	case errors.Is(err, copycat.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, copycat.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		writeError(w, http.StatusInternalServerError, "copy cataloging failed")
	}
	return true
}
