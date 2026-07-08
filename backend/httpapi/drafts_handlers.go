package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/store"
)

// draft is a per-user editor draft: an opaque client payload (op list /
// form state) keyed to a work, autosave-friendly.
type draft struct {
	ID        string          `json:"id"`
	WorkID    string          `json:"workId,omitempty"`
	Body      json.RawMessage `json:"body"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

const draftTTL = 90 * 24 * time.Hour

func draftKey(email, id string) store.Key {
	return store.Key{PK: "DRAFT#" + email, SK: id}
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
		suffix := make([]byte, 8)
		_, _ = rand.Read(suffix)
		d.ID = hex.EncodeToString(suffix)
		d.UpdatedAt = time.Now().UTC()
		data, _ := json.Marshal(d)
		rec := store.Record{Key: draftKey(id.Email, d.ID), Data: data, ExpireAt: time.Now().Add(draftTTL)}
		if _, err := db.Put(r.Context(), rec, store.CondIfAbsent); err != nil {
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
