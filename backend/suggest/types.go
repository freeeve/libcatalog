// Package suggest implements the community suggestion queue: an ephemeral
// store aggregating anonymous patron suggestions and flags per (work, term)
// pair for staff review, generalized from qllpoc's single-vocabulary
// DynamoDB implementation onto the portable document store and arbitrary
// vocabularies (controlled TermRefs or folksonomy tags). The BIBFRAME graph
// remains the source of truth for approved assignments; nothing here is
// durable except the audit trail.
package suggest

import (
	"errors"
	"time"

	"github.com/freeeve/libcatalog/backend/vocab"
)

// SuggType distinguishes a proposal to add a term from a flag to remove one.
type SuggType string

const (
	TypeAdd    SuggType = "ADD"
	TypeRemove SuggType = "REMOVE"
)

// Status is the review lifecycle of an aggregated (work, term, type) item.
type Status string

const (
	StatusPending  Status = "PENDING"
	StatusApproved Status = "APPROVED"
	StatusRejected Status = "REJECTED"
	StatusDisputed Status = "DISPUTED"
)

// Reason is the fixed flag-reason enum -- no free text anywhere.
type Reason string

const (
	ReasonDoesNotApply Reason = "does_not_apply"
	ReasonTooBroad     Reason = "too_broad_narrower_fits"
	ReasonOutdated     Reason = "outdated_or_harmful_in_context"
	ReasonSpoiler      Reason = "spoiler"
)

// Reasons lists every valid flag reason for input validation.
var Reasons = []Reason{ReasonDoesNotApply, ReasonTooBroad, ReasonOutdated, ReasonSpoiler}

// Provenance records where a suggestion originated.
type Provenance string

const (
	ProvenancePatron    Provenance = "PATRON"
	ProvenancePipeline  Provenance = "PIPELINE" // enrichment providers (tasks/039)
	ProvenanceLibrarian Provenance = "LIBRARIAN"
)

// Sentinel errors; the API layer maps these to HTTP statuses.
var (
	ErrTombstoned  = errors.New("suggest: term was rejected for this work and may not be re-suggested")
	ErrRateLimited = errors.New("suggest: rate limit exceeded")
	ErrBadTerm     = errors.New("suggest: term not in a loaded vocabulary")
	ErrFolkBlocked = errors.New("suggest: folksonomy term is blocked")
)

// Suggestion is one aggregated (work, term, type) queue item. SupporterCount
// is the number of distinct supporter hashes; the hashes themselves live in
// separate TTL'd marker items and are never exposed.
type Suggestion struct {
	WorkID         string         `json:"workId"`
	Term           vocab.TermRef  `json:"term"`
	Type           SuggType       `json:"type"`
	Status         Status         `json:"status"`
	SupporterCount int            `json:"supporterCount"`
	ReasonCounts   map[Reason]int `json:"reasonCounts,omitempty"`
	Provenance     Provenance     `json:"provenance"`
	Confidence     float64        `json:"confidence,omitempty"`
	WorkTitle      string         `json:"workTitle,omitempty"`
	SourceRef      string         `json:"sourceRef,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
	LastActivityAt time.Time      `json:"lastActivityAt"`
	ReviewedAt     time.Time      `json:"reviewedAt,omitzero"`
	ReviewedBy     string         `json:"reviewedBy,omitempty"`
	ReviewNote     string         `json:"reviewNote,omitempty"`
	// SubstituteTerm is set when the reviewer approved a neighbouring term
	// instead of the suggested one; the publisher writes the substitute.
	SubstituteTerm *vocab.TermRef `json:"substituteTerm,omitempty"`
	PublishedAt    time.Time      `json:"publishedAt,omitzero"`
	// PublishedETag is the grain ETag that carried the change into the
	// graph (the git-commit-SHA analog for the S3 grain store).
	PublishedETag string `json:"publishedEtag,omitempty"`
}

// FolkStatus is the lifecycle of a novel folksonomy term itself (distinct
// from any per-work suggestion of it): PROPOSED terms are invisible to
// autocomplete until a moderator ACCEPTS them; BLOCKED terms cannot be
// suggested at all.
type FolkStatus string

const (
	FolkProposed FolkStatus = "PROPOSED"
	FolkAccepted FolkStatus = "ACCEPTED"
	FolkBlocked  FolkStatus = "BLOCKED"
)

// FolkTerm is the stored lifecycle record of one normalized community tag.
type FolkTerm struct {
	Term      string     `json:"term"` // normalized text = identity
	Status    FolkStatus `json:"status"`
	CreatedAt time.Time  `json:"createdAt"`
	// UseCount tracks how many suggestions cite the term (moderation signal).
	UseCount int `json:"useCount"`
}

// SubmitInput is one anonymous patron suggestion or flag. SupporterHash is
// HMAC(serverSecret, sourceIP) computed by the caller -- raw IPs never reach
// this package. Term.Label is display-only; identity is (Scheme, ID) and
// controlled terms must resolve in the vocabulary index.
type SubmitInput struct {
	WorkID        string
	Term          vocab.TermRef
	Type          SuggType
	Reason        Reason // REMOVE only
	SupporterHash string
	WorkTitle     string
	SourceRef     string
}

// SubmitResult reports the outcome of a Submit.
type SubmitResult struct {
	// Duplicate is true when this supporter already voiced this exact
	// (work, term, type) -- the call is an idempotent no-op.
	Duplicate bool
	// Disputed is true when the pair now has both ADD and REMOVE pressure.
	Disputed bool
	// FolkProposed is true when the submission's novel folksonomy term
	// entered the PROPOSED lifecycle (held for moderation).
	FolkProposed bool
}

// Caps bounds anonymous submission volume per supporter hash.
type Caps struct {
	PerDay  int
	PerHour int
	// SupporterTTL ages dedup markers off; rate counters use fixed short TTLs.
	SupporterTTL time.Duration
}

// DefaultCaps are deliberately tight -- suggesting subjects is a considered
// act, not a feed interaction.
var DefaultCaps = Caps{PerDay: 20, PerHour: 8, SupporterTTL: 90 * 24 * time.Hour}
