# 033 -- Suggestion queue (folksonomy + subject suggestions)

## Context

Port and generalize qllpoc's `internal/suggest` (aggregates, supporter dedup,
tombstones, DISPUTED reconciliation, rate/velocity counters, challenge/honeypot
anti-abuse) onto `backend/store.Store`, keyed by Work id, vocab-agnostic via
`TermRef`. Adds the folksonomy two-stage lifecycle so novel tags are moderated
before becoming suggestable.

## Scope

1. `backend/suggest/{types,submit,review,abuse}.go`: aggregate items
   `WORK#<id>/SUGG#<scheme>#<term>#<ADD|REMOVE>` with supporterCount/
   reasonCounts/status/provenance(PATRON|PIPELINE|LIBRARIAN)/confidence;
   supporter dedup markers (TTL); `REJ#` tombstones; STATUS# index items;
   dispute reconciliation. TransactWriteItems semantics recomposed as
   CondIfAbsent dedup + versioned read-modify-write + best-effort index write.
2. Anti-abuse: signed challenge tokens (min/max age), honeypot field,
   HMAC(secret, ip) hashing (never raw IPs), per-IP day/hour caps, per-work
   velocity counter.
3. Folk lifecycle: novel normalized term -> `FOLK#<norm>` PROPOSED, held for
   moderation; ACCEPTED enters suggestable set + `/v1/terms` autocomplete;
   BLOCKED tombstoned.
4. Routes: `GET /v1/challenge`, `POST /v1/suggestions` (anonymous; 202/409/429),
   `GET /v1/works/{id}/suggestions` (public counts only).

## Acceptance

- Submit -> dedup -> dispute -> tombstone -> rate-limit paths tested on MemStore.
- No raw IP or free text stored; folk PROPOSED terms invisible to autocomplete.
- Demoable milestone: public suggestion API.
