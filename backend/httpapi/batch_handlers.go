package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/freeeve/libcatalog/backend/auth"
	"github.com/freeeve/libcatalog/backend/batch"
	"github.com/freeeve/libcatalog/backend/editor"
)

// resolvePreviewLimit caps the work list carried in a resolve preview; the
// count always covers the full selection.
const resolvePreviewLimit = 50

// registerBatch mounts the batch-operations surface (tasks/047): selection
// preview, op-list runs (dry-run and execute), macros, saved queries, and
// the profile set the SPA's op builder renders from. Librarian-gated like
// the rest of the editing surface.
func registerBatch(mux *http.ServeMux, svc *batch.Service, verifier auth.TokenVerifier) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)

	mux.Handle("POST /v1/batch/resolve", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Selection batch.Selection `json:"selection"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		targets, err := svc.Resolve(r.Context(), req.Selection, id.Email)
		if writeBatchError(w, err) {
			return
		}
		preview := targets
		if len(preview) > resolvePreviewLimit {
			preview = preview[:resolvePreviewLimit]
		}
		writeJSON(w, http.StatusOK, map[string]any{"matched": len(targets), "works": preview})
	})))

	mux.Handle("POST /v1/batch/ops", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Selection batch.Selection   `json:"selection"`
			Ops       []editor.Op       `json:"ops"`
			MacroID   string            `json:"macroId"`
			Params    map[string]string `json:"params"`
			DryRun    bool              `json:"dryRun"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		ops := req.Ops
		if req.MacroID != "" {
			if len(ops) > 0 {
				writeError(w, http.StatusBadRequest, "send ops or a macroId, not both")
				return
			}
			m, err := svc.GetMacro(r.Context(), id.Email, req.MacroID)
			if writeBatchError(w, err) {
				return
			}
			ops, err = batch.ApplyParams(m, req.Params)
			if writeBatchError(w, err) {
				return
			}
		}
		result, err := svc.Run(r.Context(), req.Selection, ops, req.DryRun, id.Email)
		if writeBatchError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))

	mux.Handle("GET /v1/macros", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		macros, err := svc.ListMacros(r.Context(), id.Email)
		if writeBatchError(w, err) {
			return
		}
		if macros == nil {
			macros = []batch.Macro{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"macros": macros})
	})))

	mux.Handle("POST /v1/macros", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var m batch.Macro
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&m); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		created, err := svc.CreateMacro(r.Context(), m, id.Email)
		if writeBatchError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, created)
	})))

	mux.Handle("PUT /v1/macros/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var m batch.Macro
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&m); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		updated, err := svc.UpdateMacro(r.Context(), r.PathValue("id"), m, id.Email)
		if writeBatchError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, updated)
	})))

	mux.Handle("DELETE /v1/macros/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		if writeBatchError(w, svc.DeleteMacro(r.Context(), id.Email, r.PathValue("id"))) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))

	// Item templates (tasks/069): saved item field sets on the macros
	// sharing model; applying one pre-fills the item form.
	mux.Handle("GET /v1/item-templates", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		templates, err := svc.ListItemTemplates(r.Context(), id.Email)
		if writeBatchError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"templates": templates})
	})))

	mux.Handle("POST /v1/item-templates", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var t batch.ItemTemplate
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&t); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		created, err := svc.CreateItemTemplate(r.Context(), t, id.Email)
		if writeBatchError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, created)
	})))

	mux.Handle("PUT /v1/item-templates/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var t batch.ItemTemplate
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&t); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		updated, err := svc.UpdateItemTemplate(r.Context(), r.PathValue("id"), t, id.Email)
		if writeBatchError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, updated)
	})))

	mux.Handle("DELETE /v1/item-templates/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		if writeBatchError(w, svc.DeleteItemTemplate(r.Context(), id.Email, r.PathValue("id"))) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))

	mux.Handle("GET /v1/queries", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		queries, err := svc.ListQueries(r.Context(), id.Email)
		if writeBatchError(w, err) {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"queries": queries})
	})))

	mux.Handle("POST /v1/queries", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct{ Label, Query string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		sq, err := svc.CreateQuery(r.Context(), req.Label, req.Query, id.Email)
		if writeBatchError(w, err) {
			return
		}
		writeJSON(w, http.StatusCreated, sq)
	})))

	mux.Handle("DELETE /v1/queries/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		if writeBatchError(w, svc.DeleteQuery(r.Context(), id.Email, r.PathValue("id"))) {
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}

// writeBatchError maps batch-service errors to responses; reports whether an
// error was written.
func writeBatchError(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, batch.ErrValidation):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, batch.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, batch.ErrForbidden):
		writeError(w, http.StatusForbidden, "not the owner")
	default:
		writeError(w, http.StatusInternalServerError, "batch operation failed")
	}
	return true
}
