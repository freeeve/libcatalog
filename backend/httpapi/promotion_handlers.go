package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/publish"
	"github.com/freeeve/libcat/backend/store"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

// registerPromotions mounts the tag-promotion surface: moderators propose,
// librarians decide; approval executes the batch rewrite through the
// publisher.
func registerPromotions(mux *http.ServeMux, svc *suggest.Service, publisher GraphPublisher, verifier auth.TokenVerifier, logger *slog.Logger) {
	moderator := auth.Require(verifier, auth.RoleModerator)
	librarian := auth.Require(verifier, auth.RoleLibrarian)
	// A failed rewrite is what an operator needs to see; the cataloger gets a
	// message instead of the store's raw error.
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	mux.Handle("GET /v1/promotions", moderator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		promos, err := svc.Promotions(r.Context(), suggest.Status(r.URL.Query().Get("status")))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "list failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"promotions": promos})
	})))

	mux.Handle("POST /v1/promotions", moderator(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Tag  string        `json:"tag"`
			Term vocab.TermRef `json:"term"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		promo, err := svc.ProposePromotion(r.Context(), req.Tag, req.Term, id.Email)
		switch {
		case errors.Is(err, suggest.ErrPromotionExists):
			writeError(w, http.StatusConflict, "promotion already proposed")
		case errors.Is(err, suggest.ErrBadTerm):
			writeError(w, http.StatusBadRequest, "unusable tag or unknown term")
		case err != nil:
			writeError(w, http.StatusInternalServerError, "propose failed")
		default:
			writeJSON(w, http.StatusCreated, promo)
		}
	})))

	mux.Handle("POST /v1/promotions/decide", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		var req struct {
			Tag     string `json:"tag"`
			Approve bool   `json:"approve"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil || req.Tag == "" {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if !req.Approve {
			promo, err := svc.RejectPromotion(r.Context(), req.Tag, id.Email)
			if err != nil {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"promotion": promo, "works": 0})
			return
		}

		// Execute, then stamp. The rewrite used to run after the
		// APPROVED stamp was already durable, so a store failure partway left a
		// record the state machine could never leave: DecidePromotion refuses
		// anything not PENDING, ProposePromotion supersedes only REJECTED, and
		// there was no DELETE. The tag stayed on every work, behind a promotion
		// the queue called approved.
		//
		// PromoteTag never consults the status -- only promo.Tag and promo.Term --
		// so the pending record is all it needs. A failure now leaves the
		// promotion PENDING with its partial count recorded, the queue honest, and
		// the Approve button live. Retrying is safe without any idempotence in the
		// write itself: the rewrite loop skips works that no longer carry the tag,
		// so a second attempt resumes at the one that failed.
		promo, err := svc.GetPromotion(r.Context(), req.Tag)
		if err != nil {
			writeError(w, http.StatusNotFound, "no such promotion")
			return
		}
		if promo.Status != suggest.StatusPending {
			writeError(w, http.StatusConflict, "promotion for "+promo.Tag+" is already "+string(promo.Status))
			return
		}
		promoter, ok := publisher.(TagPromoter)
		if !ok || publisher == nil {
			decided, err := svc.ApprovePromotion(r.Context(), promo.Tag, id.Email, 0)
			if err != nil {
				writeError(w, http.StatusConflict, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"promotion": decided, "works": 0,
				"note": "publisher not configured; approved but not executed",
			})
			return
		}

		works, err := promoter.PromoteTag(r.Context(), promo, id.Email)
		if err != nil {
			// Works ahead of the failing one are already rewritten. Record how far
			// it got so the queue does not report a partial rewrite as nothing.
			if rerr := svc.RecordPromotionWorks(r.Context(), promo.Tag, works); rerr != nil {
				logger.Error("recording partial promotion progress failed", "tag", promo.Tag, "works", works, "err", rerr)
			}
			// A promotion rewrites every work carrying the tag, so this
			// is the request that touches the most records at once --
			// and it concatenated the store's raw error, blob root and
			// all, into its 500.
			if errors.Is(err, blob.ErrReadOnly) {
				writeReadOnly(w)
				return
			}
			if errors.Is(err, publish.ErrGrainConflict) {
				writeError(w, http.StatusConflict, "a record changed while the promotion ran, retry")
				return
			}
			logger.Error("tag promotion rewrite failed", "tag", promo.Tag, "rewritten", works, "err", err)
			writeError(w, http.StatusInternalServerError, "rewrite failed")
			return
		}

		decided, err := svc.ApprovePromotion(r.Context(), promo.Tag, id.Email, works)
		if err != nil {
			// The catalog is promoted; only the record of it failed. Reporting 200
			// here would print works=0 over a rewrite that happened -- the very
			// lie this task is about. The promotion stays PENDING, so a retry
			// rewrites nothing (every work has lost the tag) and stamps it.
			logger.Error("promotion applied but not recorded", "tag", promo.Tag, "works", works, "err", err)
			writeError(w, http.StatusInternalServerError, "promotion applied but not recorded; approve again to record it")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"promotion": decided, "works": works})
	})))

	// The escape hatch for a promotion the one-way state machine cannot leave
	//: notably the record a deployment with no publisher wired
	// approves but never executes. Deleting frees the tag to be proposed again.
	mux.Handle("DELETE /v1/promotions/{tag}", librarian(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := auth.FromContext(r.Context())
		tag, err := vocab.NormalizeFolk(r.PathValue("tag"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "unusable tag")
			return
		}
		if err := svc.DeletePromotion(r.Context(), tag, id.Email); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "no such promotion")
				return
			}
			logger.Error("promotion delete failed", "tag", tag, "err", err)
			writeError(w, http.StatusInternalServerError, "delete failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})))
}

// TagPromoter is the promotion-execution capability of the publisher.
type TagPromoter interface {
	PromoteTag(ctx context.Context, promo suggest.Promotion, actor string) (int, error)
}

// registerTags mounts the tag typeahead: distinct tags across the grain tree
// with carry counts, substring-matched -- the convergence nudge for TagInput.
func registerTags(mux *http.ServeMux, wl *worksList, verifier auth.TokenVerifier) {
	staff := auth.Require(verifier, auth.RoleModerator)
	mux.Handle("GET /v1/tags", staff(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		all, err := wl.summaries(r)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "scan failed")
			return
		}
		q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
		counts := map[string]int{}
		for _, s := range all {
			for _, tag := range s.Tags {
				if q == "" || strings.Contains(strings.ToLower(tag), q) {
					counts[tag]++
				}
			}
		}
		type tagCount struct {
			Tag   string `json:"tag"`
			Count int    `json:"count"`
		}
		out := make([]tagCount, 0, len(counts))
		for tag, n := range counts {
			out = append(out, tagCount{tag, n})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Tag < out[j].Tag
		})
		if len(out) > 25 {
			out = out[:25]
		}
		writeJSON(w, http.StatusOK, map[string]any{"tags": out})
	})))
}
