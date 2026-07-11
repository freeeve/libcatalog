package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/authoritiesvc"
	"github.com/freeeve/libcat/backend/profilesvc"
	"github.com/freeeve/libcat/backend/publish"
	"github.com/freeeve/libcat/backend/vocab"
)

// authorityView is the read shape of a local authority term.
type authorityView struct {
	ID   string                 `json:"id"`
	ETag string                 `json:"etag"`
	Term bibframe.AuthorityTerm `json:"term"`
}

// registerAuthorities mounts the librarian authorities surface (tasks/046):
// local-term CRUD with ETag optimistic locking, the profile the editor form
// renders from, merge, and the explicit index reload.
func registerAuthorities(mux *http.ServeMux, svc *authoritiesvc.Service, prof *profilesvc.Service, verifier auth.TokenVerifier, logger *slog.Logger) {
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	// A merge that failed at the store is what an operator needs to see; the
	// cataloger gets a message instead of the blob root (tasks/272).
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	// The editing form's field definitions -- the same live profile set the
	// record editor maps through, so a runtime edit to authority-topic shows
	// here without a restart.
	mux.Handle("GET /v1/authorities/profile", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, prof.Set()["authority-topic"])
	})))

	// Listing and label search over the local scheme (imported schemes are
	// served by /v1/terms; this surface manages what the deployment owns).
	mux.Handle("GET /v1/authorities", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		limit := termsDefaultLimit
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		var terms []*vocab.Term
		if q == "" {
			terms = svc.Vocab.Terms(authoritiesvc.LocalScheme)
		} else {
			terms = svc.Vocab.Search(authoritiesvc.LocalScheme, q, limit)
		}
		// A node with no labels is not a heading -- it is grain debris (a
		// marker asserted on a subject nothing describes, tasks/202) and
		// must not render as a blank row in the Authorities screen.
		kept := make([]*vocab.Term, 0, len(terms))
		for _, t := range terms {
			if len(t.Labels) > 0 {
				kept = append(kept, t)
			}
		}
		terms = kept
		// total is the true count of local headings (empty-query browse) or of
		// the matches returned (label search), so the screen can report a real
		// number and know when it is showing a truncated page rather than
		// presenting the fetch limit as a total (tasks/329).
		total := len(terms)
		if q == "" && len(terms) > limit {
			terms = terms[:limit]
		}
		writeJSON(w, http.StatusOK, map[string]any{"terms": terms, "total": total})
	})))

	mux.Handle("POST /v1/authorities", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var term bibframe.AuthorityTerm
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&term); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		term.MergedInto = "" // retirement is merge's to write, never a client field
		authID, etag, err := svc.Create(r.Context(), term, id.Email)
		if errors.Is(err, authoritiesvc.ErrValidation) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authority create failed")
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusCreated, map[string]string{
			"id": authID, "uri": bibframe.LocalAuthorityIRI(authID), "etag": etag,
		})
	})))

	mux.Handle("GET /v1/authorities/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authID := r.PathValue("id")
		if !authoritiesvc.IDPattern.MatchString(authID) {
			writeError(w, http.StatusBadRequest, "bad authority id")
			return
		}
		term, etag, err := svc.Get(r.Context(), authID)
		if errors.Is(err, blob.ErrNotFound) {
			writeError(w, http.StatusNotFound, "no such authority")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "authority read failed")
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, authorityView{ID: authID, ETag: etag, Term: term})
	})))

	mux.Handle("PUT /v1/authorities/{id}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		authID := r.PathValue("id")
		if !authoritiesvc.IDPattern.MatchString(authID) {
			writeError(w, http.StatusBadRequest, "bad authority id")
			return
		}
		ifMatch := r.Header.Get("If-Match")
		if ifMatch == "" {
			writeError(w, http.StatusPreconditionRequired, "If-Match required")
			return
		}
		var term bibframe.AuthorityTerm
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&term); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		term.MergedInto = "" // retirement is merge's to write, never a client field (mirror POST); Update restores the prior grain's value
		etag, err := svc.Update(r.Context(), authID, term, ifMatch, id.Email)
		switch {
		case errors.Is(err, authoritiesvc.ErrValidation):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, blob.ErrNotFound):
			writeError(w, http.StatusNotFound, "no such authority")
			return
		case errors.Is(err, blob.ErrPreconditionFailed):
			// A concurrent write landed: return the fresh state so the
			// client rebases deliberately, mirroring the records surface.
			fresh, freshTag, err := svc.Get(r.Context(), authID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "authority read failed")
				return
			}
			w.Header().Set("ETag", freshTag)
			writeJSON(w, http.StatusPreconditionFailed, authorityView{ID: authID, ETag: freshTag, Term: fresh})
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "authority write failed")
			return
		}
		w.Header().Set("ETag", etag)
		writeJSON(w, http.StatusOK, map[string]string{"id": authID, "etag": etag})
	})))

	mux.Handle("POST /v1/authorities/merge", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Loser  string        `json:"loser"`
			Winner vocab.TermRef `json:"winner"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			!authoritiesvc.IDPattern.MatchString(req.Loser) || req.Winner.ID == "" || req.Winner.Scheme == "" {
			writeError(w, http.StatusBadRequest, "merge needs a local loser id and a winner term")
			return
		}
		result, err := svc.Merge(r.Context(), req.Loser, req.Winner, id.Email)
		switch {
		case errors.Is(err, authoritiesvc.ErrValidation):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case errors.Is(err, blob.ErrNotFound):
			writeError(w, http.StatusNotFound, "no such authority")
			return
		case errors.Is(err, blob.ErrReadOnly):
			writeReadOnly(w)
			return
		case errors.Is(err, publish.ErrGrainConflict):
			writeError(w, http.StatusConflict, "the record changed while the merge ran, retry")
			return
		case err != nil:
			// Merge writes through publish.MutateGrain once per carrying Work and
			// once for the loser, so a store failure used to answer 409 with an
			// *os.PathError as its body: the wrong status, claiming a concurrent
			// edit, and the wrong message, naming the blob root (tasks/272). Every
			// unclassified error took that same 409 path -- a read failure on the
			// loser grain, a SummariesOf failure -- so 272's fix stopped one case
			// short. They are server faults; answer 500 and log the cause.
			//
			// "merge failed" was also not true (tasks/305). The rewrite runs before
			// the retirement, so a failure leaves the heading live and every work
			// either repointed or not; the works already rewritten stay rewritten,
			// and re-issuing the same merge resumes at the one that failed. Say the
			// count and say what to do, or the only reasonable reading is "nothing
			// happened" and a half-merged catalog goes uninvestigated.
			logger.Error("authority merge failed", "loser", req.Loser, "winner", req.Winner.ID,
				"rewritten", result.Rewritten, "carriers", result.Carriers, "complete", result.Complete, "err", err)
			msg := "merge failed; nothing was changed"
			switch {
			case result.Complete:
				msg = "merge applied, but the vocabulary index was not reloaded; it is correct on disk"
			case result.Rewritten > 0:
				msg = fmt.Sprintf("merge partially applied: %d of %d works rewritten; the heading is still live, retry to finish",
					result.Rewritten, result.Carriers)
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error":     msg,
				"loser":     result.Loser,
				"winner":    result.Winner,
				"rewritten": result.Rewritten,
				"carriers":  result.Carriers,
				"complete":  result.Complete,
			})
			return
		}
		writeJSON(w, http.StatusOK, result)
	})))

	mux.Handle("POST /v1/authorities/reload", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := svc.Reload(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "vocab reload failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"schemes": svc.Vocab.Schemes()})
	})))
}
