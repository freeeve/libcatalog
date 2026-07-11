package httpapi

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"

	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/vocab"
)

// Input patterns, ported from qllpoc: every field is enum- or pattern-gated.
var (
	workIDPattern    = regexp.MustCompile(`^w[a-z0-9]{6,20}$`)
	sourceRefPattern = regexp.MustCompile(`^[a-z]{1,20}:[A-Za-z0-9-]{1,40}$`)
	schemePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}$`)
)

const maxTitleLen = 300

type suggestionRequest struct {
	WorkID    string        `json:"workId"`
	Term      vocab.TermRef `json:"term"`
	Type      string        `json:"type"`
	Reason    string        `json:"reason,omitempty"`
	WorkTitle string        `json:"workTitle,omitempty"`
	SourceRef string        `json:"sourceRef,omitempty"`
	Challenge string        `json:"challenge"`
	// Website is the honeypot: real clients send it empty; any value gets a
	// silent 202 with no write.
	Website string `json:"website"`
}

// registerSuggestions mounts the anonymous patron surface: challenge tokens,
// suggestion submission, and public per-work counts.
func registerSuggestions(mux *http.ServeMux, svc *suggest.Service, abuse *suggest.Abuse) {
	mux.HandleFunc("GET /v1/challenge", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"challenge": abuse.Challenge()})
	})

	// Anonymous report-a-problem: same challenge, honeypot,
	// and rate budget as term suggestions; the note lands in the review
	// queue as a CONCERN and never touches the graph.
	mux.HandleFunc("POST /v1/concerns", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			WorkID    string `json:"workId"`
			Note      string `json:"note"`
			WorkTitle string `json:"workTitle"`
			Challenge string `json:"challenge"`
			Website   string `json:"website"` // honeypot
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if req.Website != "" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if !abuse.VerifyChallenge(req.Challenge) {
			writeError(w, http.StatusBadRequest, "bad or expired challenge")
			return
		}
		if !workIDPattern.MatchString(req.WorkID) || len(req.WorkTitle) > maxTitleLen {
			writeError(w, http.StatusBadRequest, "bad field")
			return
		}
		err := svc.SubmitConcern(r.Context(), req.WorkID, req.Note, req.WorkTitle, abuse.HashIP(clientIP(r)))
		switch {
		case errors.Is(err, suggest.ErrRateLimited):
			writeError(w, http.StatusTooManyRequests, "rate limited")
		case err != nil:
			writeError(w, http.StatusBadRequest, "declined")
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	})

	mux.HandleFunc("POST /v1/suggestions", func(w http.ResponseWriter, r *http.Request) {
		var req suggestionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body")
			return
		}
		if req.Website != "" {
			// Honeypot tripped: indistinguishable success, no write.
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if !abuse.VerifyChallenge(req.Challenge) {
			writeError(w, http.StatusBadRequest, "bad or expired challenge")
			return
		}
		if !workIDPattern.MatchString(req.WorkID) ||
			!schemePattern.MatchString(req.Term.Scheme) ||
			req.Term.ID == "" || len(req.Term.ID) > 400 ||
			len(req.WorkTitle) > maxTitleLen ||
			(req.SourceRef != "" && !sourceRefPattern.MatchString(req.SourceRef)) {
			writeError(w, http.StatusBadRequest, "bad field")
			return
		}
		in := suggest.SubmitInput{
			WorkID:        req.WorkID,
			Term:          req.Term,
			Type:          suggest.SuggType(req.Type),
			Reason:        suggest.Reason(req.Reason),
			SupporterHash: abuse.HashIP(clientIP(r)),
			WorkTitle:     req.WorkTitle,
			SourceRef:     req.SourceRef,
		}
		result, err := svc.Submit(r.Context(), in)
		switch {
		case errors.Is(err, suggest.ErrTombstoned), errors.Is(err, suggest.ErrFolkBlocked):
			writeError(w, http.StatusConflict, "declined")
		case errors.Is(err, suggest.ErrRateLimited):
			writeError(w, http.StatusTooManyRequests, "rate limited")
		// Patron-policy refusals carry their own message so the
		// client can explain why -- suggestions off, scheme not open, or the
		// free-text rule -- rather than a bare "declined".
		case errors.Is(err, suggest.ErrSuggestionsOff),
			errors.Is(err, suggest.ErrSchemeNotAllowed),
			errors.Is(err, suggest.ErrFreeTextOff),
			errors.Is(err, suggest.ErrNovelTagOff):
			writeError(w, http.StatusForbidden, err.Error())
		case errors.Is(err, suggest.ErrBadTerm):
			writeError(w, http.StatusBadRequest, "unknown term")
		case err != nil:
			writeError(w, http.StatusBadRequest, "invalid suggestion")
		default:
			writeJSON(w, http.StatusAccepted, map[string]any{
				"duplicate":    result.Duplicate,
				"disputed":     result.Disputed,
				"folkProposed": result.FolkProposed,
			})
		}
	})

	mux.HandleFunc("GET /v1/works/{id}/suggestions", func(w http.ResponseWriter, r *http.Request) {
		workID := r.PathValue("id")
		if !workIDPattern.MatchString(workID) {
			writeError(w, http.StatusBadRequest, "bad work id")
			return
		}
		items, err := svc.ForWork(r.Context(), workID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "lookup failed")
			return
		}
		type publicSuggestion struct {
			Term           vocab.TermRef          `json:"term"`
			Type           suggest.SuggType       `json:"type"`
			Status         suggest.Status         `json:"status"`
			SupporterCount int                    `json:"supporterCount"`
			ReasonCounts   map[suggest.Reason]int `json:"reasonCounts,omitempty"`
		}
		out := make([]publicSuggestion, 0, len(items))
		for _, sg := range items {
			out = append(out, publicSuggestion{
				Term: sg.Term, Type: sg.Type, Status: sg.Status,
				SupporterCount: sg.SupporterCount, ReasonCounts: sg.ReasonCounts,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"workId": workID, "suggestions": out})
	})
}

// clientIP prefers the CloudFront viewer header (un-spoofable when the API
// sits behind the CDN), falling back to the socket peer.
func clientIP(r *http.Request) string {
	if v := r.Header.Get("Cloudfront-Viewer-Address"); v != "" {
		if host, _, err := net.SplitHostPort(v); err == nil {
			return host
		}
		return v
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
