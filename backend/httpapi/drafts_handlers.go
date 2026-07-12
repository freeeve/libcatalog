package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
)

// draft is a per-user editor draft: an opaque client payload (op list /
// form state) keyed to a work, autosave-friendly. One draft slot per (user,
// work): the id IS the work id, so a work can never accumulate several drafts
// for one user. Body is omitempty so the list projection can drop
// it -- the point read carries the body.
type draft struct {
	ID        string          `json:"id"`
	WorkID    string          `json:"workId,omitempty"`
	Body      json.RawMessage `json:"body,omitempty"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

const draftTTL = 90 * 24 * time.Hour

// draftKey scopes a draft to a (user, work) pair. The work id in the sort key
// is what makes the slot real: one key per work, so uniqueness is structural
// rather than something a scan has to enforce.
func draftKey(email, workID string) store.Key {
	return store.Key{PK: "DRAFT#" + email, SK: "W#" + workID}
}

// registerDrafts mounts per-user draft CRUD (librarian-gated like the rest
// of the editing surface).
func registerDrafts(mux *http.ServeMux, db store.Store, librarian func(http.Handler) http.Handler) {
	mux.Handle("POST /v1/drafts", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var d draft
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&d); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if d.WorkID == "" {
			writeError(w, http.StatusBadRequest, "draft needs a workId")
			return
		}
		// The id is the work id: one slot per (user, work). Upsert rather than
		// reject a second write so a second tab's autosave lands last-writer-
		// wins on the shared slot instead of erroring.
		d.ID = d.WorkID
		d.UpdatedAt = time.Now().UTC()
		data, _ := json.Marshal(d)
		rec := store.Record{Key: draftKey(id.Email, d.WorkID), Data: data, ExpireAt: time.Now().Add(draftTTL)}
		if _, err := db.Put(r.Context(), rec, store.CondNone); err != nil {
			writeError(w, http.StatusInternalServerError, "draft save failed")
			return
		}
		writeJSON(w, http.StatusCreated, d)
	})))

	mux.Handle("GET /v1/drafts", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		drafts := []draft{}
		for rec, err := range db.Query(r.Context(), "DRAFT#"+id.Email, "", store.QueryOpt{}) {
			if err != nil {
				writeError(w, http.StatusInternalServerError, "draft list failed")
				return
			}
			var d draft
			if json.Unmarshal(rec.Data, &d) == nil {
				// The list is a hot-path call on every editor open; drop the
				// body so it never fans out a megabyte per draft. The point
				// read carries the body for the one draft the editor wants.
				d.Body = nil
				drafts = append(drafts, d)
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"drafts": drafts})
	})))

	mux.Handle("GET /v1/drafts/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		rec, err := db.Get(r.Context(), draftKey(id.Email, r.PathValue("id")))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such draft")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "draft read failed")
			return
		}
		var d draft
		_ = json.Unmarshal(rec.Data, &d)
		writeJSON(w, http.StatusOK, d)
	})))

	mux.Handle("PUT /v1/drafts/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var d draft
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&d); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		d.ID = r.PathValue("id")
		d.UpdatedAt = time.Now().UTC()
		key := draftKey(id.Email, d.ID)
		rec, err := db.Get(r.Context(), key)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such draft")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "draft read failed")
			return
		}
		// The slot invariant: the draft's id IS its work id (POST enforces
		// it; the list links drafts to works through it). A PUT omitting
		// workId keeps the slot's linkage rather than blanking it -- which
		// also heals drafts an older build blanked -- and a PUT naming a
		// DIFFERENT work is a client error, not a re-file.
		if d.WorkID == "" {
			d.WorkID = d.ID
		} else if d.WorkID != d.ID {
			writeError(w, http.StatusBadRequest, "workId cannot change: a draft's slot is its work")
			return
		}
		data, _ := json.Marshal(d)
		rec.Data = data
		rec.ExpireAt = time.Now().Add(draftTTL)
		if _, err := db.Put(r.Context(), rec, store.CondNone); err != nil {
			writeError(w, http.StatusInternalServerError, "draft save failed")
			return
		}
		writeJSON(w, http.StatusOK, d)
	})))

	mux.Handle("DELETE /v1/drafts/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		err := db.Delete(r.Context(), store.Record{Key: draftKey(id.Email, r.PathValue("id"))}, store.CondNone)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such draft")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "draft delete failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}
