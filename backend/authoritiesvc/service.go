// Package authoritiesvc is the local-authority editing service:
// CRUD over authority grains for a deployment's own headings, authority
// merge with corpus-wide reference rewrite, and the on-save auto-linker that
// turns string subjects into moderated linking suggestions. It is SKOS-native
// Koha-authorities: cross-references are altLabel (used-for) and broader/
// narrower/related; global heading update is free because bibs reference
// terms by URI, so a relabel propagates at projection.
package authoritiesvc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"time"

	"github.com/freeeve/libcat/bibframe"
	"github.com/freeeve/libcat/identity"
	"github.com/freeeve/libcat/ingest"
	"github.com/freeeve/libcat/storage/blob"

	"github.com/freeeve/libcat/backend/publish"
	"github.com/freeeve/libcat/backend/suggest"
	"github.com/freeeve/libcat/backend/trigger"
	"github.com/freeeve/libcat/backend/vocab"
)

// LocalScheme is the vocabulary key local authority terms live under: their
// statements land in the authority:local named graph and the vocab index
// serves them beside imported schemes.
const LocalScheme = "local"

// IDPattern matches a minted local-authority id (identity.Mint "a" prefix).
var IDPattern = regexp.MustCompile(`^a[0-9a-v]{6,20}$`)

// ErrValidation reports a term description that cannot be saved.
var ErrValidation = errors.New("authoritiesvc: invalid term")

// Auto-link confidence by match kind: an exact preferred-label match is
// near-certain; a used-for (alt label) match is likelier to need review.
const (
	confPrefMatch = 0.9
	confAltMatch  = 0.75
)

// Service edits local authority grains and keeps the shared vocab index
// fresh. All writes go through the store's optimistic concurrency.
type Service struct {
	Blob  blob.Store
	Vocab *vocab.Index
	// Queue, when set, receives audit entries and the auto-linker's
	// PIPELINE suggestions.
	Queue *suggest.Service
	// Trigger, when set, gets one grains-changed event per merge rewrite.
	Trigger trigger.Notifier
	// Prefix is the grain tree root in the blob store ("" = repo-layout
	// paths straight from bibframe paths).
	Prefix string
	// AuthoritiesPrefix is the vocab reload scan prefix; empty means
	// Prefix + "data/authorities/".
	AuthoritiesPrefix string
	// Schemes filters the vocab reload (nil = every authority graph).
	Schemes []string
	// SchemesFn, when set, supersedes Schemes -- the seam: installed
	// vocabulary snapshots widen the effective filter at reload time, so an
	// authority edit's reload never drops an installed scheme.
	SchemesFn func(context.Context) ([]string, error)
	// Summaries, when set, is the shared maintained summary source
	// (workindex) merge rewrites scan instead of a per-run
	// corpus walk; nil falls back to ScanSummaries.
	Summaries ingest.SummarySource
	Logger    *slog.Logger
}

// MergeResult summarizes one authority merge.
type MergeResult struct {
	Loser  string `json:"loser"`
	Winner string `json:"winner"`
	// Rewritten counts the Work grains repointed at the winner. On a failure it
	// is what the pass managed before it stopped, and it is meaningful: the works
	// it counts really are repointed.
	Rewritten int `json:"rewritten"`
	// Carriers is how many Works named the loser when the pass began. Rewritten <
	// Carriers means the merge did not finish and the heading is still live; the
	// same request run again resumes, because the loop skips works that no longer
	// name the loser.
	Carriers int `json:"carriers"`
	// Complete reports that every carrier was rewritten AND the loser was retired.
	// It distinguishes the two ways Merge can return an error with work behind it:
	// a rewrite that stopped partway (Complete false, retry to finish) from a
	// finished merge whose index reload failed (Complete true, nothing to redo).
	Complete bool `json:"complete"`
}

// Create mints a local authority id, writes the term's grain, and refreshes
// the index. The term's URI field is assigned, never client-chosen.
func (s *Service) Create(ctx context.Context, term bibframe.AuthorityTerm, actor string) (id, etag string, err error) {
	if err := validateTerm(term); err != nil {
		return "", "", err
	}
	// Mint-and-check: an id collision surfaces as IfNoneMatch failing, so
	// retry with a fresh id (negligible in practice, cheap to rule out).
	for range 3 {
		id = identity.Mint(identity.AuthorityPrefix)
		term.URI = bibframe.LocalAuthorityIRI(id)
		grain, err := bibframe.BuildAuthorityGrain(nil, term, LocalScheme)
		if err != nil {
			return "", "", err
		}
		etag, err = s.Blob.Put(ctx, s.grainPath(id), grain, blob.PutOptions{
			IfNoneMatch: true, ContentType: "application/n-quads",
		})
		if errors.Is(err, blob.ErrPreconditionFailed) {
			continue
		}
		if err != nil {
			return "", "", err
		}
		s.audit(ctx, suggest.AuditEntry{
			Action: "AUTHORITY_CREATE", Actor: actor, ETag: etag,
			Terms: []string{LocalScheme + ":" + term.URI},
			Note:  bestLabel(term),
		})
		return id, etag, s.Reload(ctx)
	}
	return "", "", fmt.Errorf("authoritiesvc: minting kept colliding")
}

// Get reads one local authority term and its concurrency token.
func (s *Service) Get(ctx context.Context, id string) (bibframe.AuthorityTerm, string, error) {
	grain, etag, err := s.Blob.Get(ctx, s.grainPath(id))
	if err != nil {
		return bibframe.AuthorityTerm{}, "", err
	}
	term, err := bibframe.ParseAuthorityGrain(grain, bibframe.LocalAuthorityIRI(id), LocalScheme)
	return term, etag, err
}

// Update replaces a term's description under the client's If-Match token
// (blob.ErrPreconditionFailed on a concurrent write, blob.ErrNotFound if the
// term does not exist). A recorded merge retirement survives the edit.
func (s *Service) Update(ctx context.Context, id string, term bibframe.AuthorityTerm, ifMatch, actor string) (string, error) {
	if err := validateTerm(term); err != nil {
		return "", err
	}
	old, _, err := s.Blob.Get(ctx, s.grainPath(id))
	if err != nil {
		return "", err
	}
	term.URI = bibframe.LocalAuthorityIRI(id)
	if term.MergedInto == "" {
		prev, err := bibframe.ParseAuthorityGrain(old, term.URI, LocalScheme)
		if err != nil {
			return "", err
		}
		term.MergedInto = prev.MergedInto
	}
	grain, err := bibframe.BuildAuthorityGrain(old, term, LocalScheme)
	if err != nil {
		return "", err
	}
	etag, err := s.Blob.Put(ctx, s.grainPath(id), grain, blob.PutOptions{
		IfMatch: ifMatch, ContentType: "application/n-quads",
	})
	if err != nil {
		return "", err
	}
	s.audit(ctx, suggest.AuditEntry{
		Action: "AUTHORITY_EDIT", Actor: actor, ETag: etag,
		Terms: []string{LocalScheme + ":" + term.URI},
		Note:  bestLabel(term),
	})
	return etag, s.Reload(ctx)
}

// Merge retires the local term loserID into winner: lcat:mergedInto lands in
// the loser's grain, and every Work grain referencing the loser is rewritten
// to the winner in one batch pass (references live in the editorial graph,
// so the rewrite is a global heading update, not a feed mutation). The
// winner may belong to any loaded scheme -- merging a local heading into an
// established vocabulary term is the expected promotion path.
func (s *Service) Merge(ctx context.Context, loserID string, winner vocab.TermRef, actor string) (MergeResult, error) {
	loserURI := bibframe.LocalAuthorityIRI(loserID)
	// A local winner arrives as the short minted id (what POST
	// /v1/authorities returns); expand it so the self-merge guard compares
	// like with like -- it used to compare the short id against the
	// expanded loser IRI and never matched, letting a term merge into
	// itself and silently retire. Expansion also makes the
	// stored marker, the rewrites, and the winnerSubject lookup carry the
	// canonical IRI, as every non-local winner already does.
	if winner.Scheme == LocalScheme && IDPattern.MatchString(winner.ID) {
		winner.ID = bibframe.LocalAuthorityIRI(winner.ID)
	}
	if winner.ID == "" || winner.ID == loserURI {
		return MergeResult{}, fmt.Errorf("%w: merge needs a distinct winner term", ErrValidation)
	}
	loserPath := s.grainPath(loserID)
	loserGrain, _, err := s.Blob.Get(ctx, loserPath)
	if err != nil {
		return MergeResult{}, err
	}
	// The grain is keyed by short id, so Get succeeds for any minted id --
	// but the marker must land on a subject the grain actually describes,
	// or the merge mints a phantom labelless node (pre-rename /
	// imported grains carry a different IRI base than the id-derived one).
	if !bibframe.AuthorityGrainDescribes(loserGrain, loserURI) {
		return MergeResult{}, fmt.Errorf("%w: authority grain for %s does not describe %s -- namespace mismatch", ErrValidation, loserID, loserURI)
	}
	// A term already retired into a survivor may not be merged into a different
	// one: AddAuthorityMergeMarker appends, so a second marker would leave the
	// loser with two contradictory mergedInto statements whose effective winner is
	// nondeterministic (quad order), and the re-merge would move no works (the
	// first merge already repointed the carriers off the loser). Refuse it,
	// symmetric to the self-merge and namespace guards. Re-merging into
	// the same winner stays idempotent.
	if prior, err := bibframe.ParseAuthorityGrain(loserGrain, loserURI, LocalScheme); err != nil {
		return MergeResult{}, err
	} else if prior.MergedInto != "" && prior.MergedInto != winner.ID {
		return MergeResult{}, fmt.Errorf("%w: term %s is already merged into %s; it cannot be merged again", ErrValidation, loserID, prior.MergedInto)
	}
	subject := s.winnerSubject(winner)
	summaries, paths, err := ingest.SummariesOf(ctx, s.Summaries, s.Blob, s.Prefix+"data/works/")
	if err != nil {
		return MergeResult{}, err
	}
	// Carriers is fixed before the first write, so a failure can say "1 of 2"
	// rather than "1", and a retry's "1 of 1" reads as the resume it is.
	var carriers []ingest.WorkSummary
	for _, summary := range summaries {
		if slices.Contains(summary.Subjects, loserURI) {
			carriers = append(carriers, summary)
		}
	}
	result := MergeResult{Loser: loserURI, Winner: winner.ID, Carriers: len(carriers)}

	// Rewrite the works, THEN retire the loser. The marker is a
	// durable claim that every carrying Work now points at the winner; writing it
	// first meant a rewrite that failed partway left that claim standing over a
	// catalog describing one concept two ways -- some works on the winner, the
	// rest on a heading marked retired. Nothing in the loop needs the marker: it
	// matches on loserURI and writes subject, neither of which comes from the
	// loser grain. Marking last leaves a failure fully resumable by re-issuing the
	// same merge, which is what the handler now tells the operator to do.
	//
	// Same execute-then-stamp shape as the promotion.
	var changed []string
	finish := func(err error) (MergeResult, error) {
		// Both outcomes are audited and both reload. The audit entry is the only
		// record that a heading was touched and N works repointed, and it used to
		// be written on the success path only -- absent for exactly the runs where
		// somebody needs it. The index used to be rebuilt on the success path only,
		// so a failed merge left the process contradicting its own store.
		note := fmt.Sprintf("%d works rewritten", result.Rewritten)
		if err != nil && !result.Complete {
			note = fmt.Sprintf("partial: rewrote %d of %d works, then failed; heading not retired, retry to finish",
				result.Rewritten, result.Carriers)
		}
		s.audit(ctx, suggest.AuditEntry{
			Action: "AUTHORITY_MERGE", Actor: actor,
			Terms: []string{LocalScheme + ":" + loserURI, winner.Scheme + ":" + winner.ID},
			Note:  note,
		})
		if s.Trigger != nil && len(changed) > 0 {
			_ = s.Trigger.Notify(ctx, trigger.Event{Kind: "grains-changed", Paths: changed, At: time.Now().UTC()})
		}
		rerr := s.Reload(ctx)
		if err != nil {
			return result, err
		}
		return result, rerr
	}

	for _, summary := range carriers {
		path := paths[summary.WorkID]
		workID := summary.WorkID
		if _, err := publish.MutateGrain(ctx, s.Blob, path, func(old []byte) ([]byte, error) {
			return bibframe.ReplaceSubjectReference(old, workID, loserURI, subject, winner.Scheme)
		}); err != nil {
			return finish(fmt.Errorf("rewrite %s: %w", workID, err))
		}
		result.Rewritten++
		changed = append(changed, path)
	}
	if _, err := publish.MutateGrain(ctx, s.Blob, loserPath, func(old []byte) ([]byte, error) {
		return bibframe.AddAuthorityMergeMarker(old, loserURI, winner.ID, LocalScheme)
	}); err != nil {
		return finish(err)
	}
	changed = append(changed, loserPath)
	result.Complete = true
	return finish(nil)
}

// AutoLink matches a just-saved Work's uncontrolled string subjects against
// the authority labels of every loaded scheme and lands whole-heading
// matches in the moderation queue as PIPELINE suggestions -- never writing
// the link itself. Returns the number of candidate links handed
// to the queue; the queue is create-only and tombstone-aware, so re-running
// on every save never duplicates, and a tag whose term the Work already
// carries as a controlled subject produces no candidate.
func (s *Service) AutoLink(ctx context.Context, workID string, grain []byte) (int, error) {
	if s.Queue == nil || s.Vocab == nil {
		return 0, nil
	}
	summaries, err := ingest.SummarizeGrain(grain)
	if err != nil {
		return 0, err
	}
	schemes := s.Vocab.Schemes()
	enqueued := 0
	for _, summary := range summaries {
		if summary.WorkID != workID {
			continue
		}
		for _, tag := range summary.Tags {
			for _, scheme := range schemes {
				for _, m := range s.Vocab.MatchLabel(scheme, tag) {
					if slices.Contains(summary.Subjects, m.Term.ID) {
						continue
					}
					confidence := confPrefMatch
					if m.Alt {
						confidence = confAltMatch
					}
					ref := vocab.TermRef{Scheme: scheme, ID: m.Term.ID, Label: m.Term.Label("en")}
					if err := s.Queue.PipelineSuggest(ctx, workID, ref, confidence); err != nil {
						return enqueued, err
					}
					enqueued++
				}
			}
		}
	}
	return enqueued, nil
}

// Reload rebuilds the shared vocab index from the authorities tree so edits
// are searchable immediately.
func (s *Service) Reload(ctx context.Context) error {
	if s.Vocab == nil {
		return nil
	}
	prefix := s.AuthoritiesPrefix
	if prefix == "" {
		prefix = s.Prefix + "data/authorities/"
	}
	schemes := s.Schemes
	if s.SchemesFn != nil {
		var err error
		if schemes, err = s.SchemesFn(ctx); err != nil {
			if s.Logger != nil {
				s.Logger.Error("scheme resolution failed", "err", err)
			}
			return err
		}
	}
	if err := s.Vocab.Reload(ctx, s.Blob, prefix, schemes); err != nil {
		if s.Logger != nil {
			s.Logger.Error("vocab reload failed", "err", err)
		}
		return err
	}
	return nil
}

// winnerSubject resolves the merge winner's full description (labels,
// broader links) from the index, falling back to the caller's label.
func (s *Service) winnerSubject(winner vocab.TermRef) bibframe.AuthoritySubject {
	if s.Vocab != nil {
		if t, ok := s.Vocab.Lookup(winner.Scheme, winner.ID); ok {
			return bibframe.AuthoritySubject{URI: t.ID, Labels: t.Labels, Broader: t.Broader}
		}
	}
	labels := map[string]string{}
	if winner.Label != "" {
		labels[""] = winner.Label
	}
	return bibframe.AuthoritySubject{URI: winner.ID, Labels: labels}
}

func (s *Service) grainPath(id string) string {
	return s.Prefix + bibframe.AuthorityGrainPath(id)
}

func (s *Service) audit(ctx context.Context, entry suggest.AuditEntry) {
	if s.Queue != nil {
		s.Queue.WriteAudit(ctx, entry)
	}
}

// validateTerm enforces the authority-topic profile's floor: a term needs at
// least one non-empty preferred label.
func validateTerm(term bibframe.AuthorityTerm) error {
	for _, label := range term.PrefLabel {
		if label != "" {
			return nil
		}
	}
	return fmt.Errorf("%w: a preferred label is required", ErrValidation)
}

func bestLabel(term bibframe.AuthorityTerm) string {
	return vocab.PickLabel(term.PrefLabel)
}
