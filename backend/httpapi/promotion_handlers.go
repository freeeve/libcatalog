package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/freeeve/libcat/backend/auth"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

// registerPromotions mounts the tag-promotion surface: moderators propose,
// librarians decide; approval executes the batch rewrite through the
// publisher.
func registerPromotions(mux *http.ServeMux, svc *suggest.Service, publisher GraphPublisher, verifier auth.TokenVerifier) {
	moderator := auth.Require(verifier, auth.RoleModerator)
	librarian := auth.Require(verifier, auth.RoleLibrarian)

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
		promo, err := svc.DecidePromotion(r.Context(), req.Tag, req.Approve, id.Email)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		resp := map[string]any{"promotion": promo, "works": 0}
		if req.Approve {
			if promoter, ok := publisher.(TagPromoter); ok && publisher != nil {
				works, err := promoter.PromoteTag(r.Context(), promo, id.Email)
				if err != nil {
					writeError(w, http.StatusInternalServerError, "rewrite failed: "+err.Error())
					return
				}
				_ = svc.MarkPromotionExecuted(r.Context(), promo.Tag, works)
				resp["works"] = works
			} else {
				resp["note"] = "publisher not configured; approved but not executed"
			}
		}
		writeJSON(w, http.StatusOK, resp)
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
