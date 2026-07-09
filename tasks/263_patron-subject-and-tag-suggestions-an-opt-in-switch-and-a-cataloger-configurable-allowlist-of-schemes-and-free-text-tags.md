# 263 -- patron subject and tag suggestions: an opt-in switch and a cataloger-configurable allowlist of schemes and free-text tags

Opened 2026-07-09, from the maintainer:

> one use case i wanted to handle was being able to suggest subjects and tags
> (an opt-in feature). the catalogers would see a queue of suggested subjects
> and tags, and configure which subjects / controlled vocabularies / or
> free-text tags should be allowed.

## What already exists

Most of the machinery is built. This task is the two missing halves, not the
feature.

- **Patrons can suggest** controlled terms and free-text tags:
  `POST /v1/suggestions`, rate-limited and challenge-gated (`suggest/abuse.go`).
- **Catalogers see a queue**: `GET /v1/queue`, `Queue.svelte`, batch decisions
  through `POST /v1/review` with approve/reject/tombstone, and `PublishBar`.
- **Free-text tags have a lifecycle**: `vocab.FolkScheme` terms carry a use
  count, and `suggest/promotion.go` promotes a folk tag to a controlled term
  once a cataloger maps it.
- **Which vocabularies load** is `LCATD_VOCAB_SCHEMES`.

## What is missing

### 1. It is not opt-in

`httpapi.go:140` mounts the whole anonymous suggestion surface whenever
`Suggest` and `Abuse` are both wired:

```go
if deps.Suggest != nil && deps.Abuse != nil {
    registerSuggestions(mux, deps.Suggest, deps.Abuse)
}
```

`LCATD_ABUSE_SECRET` is required for lcatd to start at all, so in practice every
deployment that wants a review queue also exposes anonymous suggestion. A
library that wants staff-only enrichment has no switch. The queue and the
suggestion intake are one feature today; they are two.

### 2. There is no allowlist -- only "whatever vocabulary happens to be loaded"

`suggest/service.go:139` accepts any term the vocab index can resolve:

```go
if ref.Scheme != vocab.FolkScheme {
    term, ok := s.vocab.Lookup(ref.Scheme, ref.ID)
    if !ok { return ref, false, ErrBadTerm }
```

So the set of suggestible schemes is a side effect of `LCATD_VOCAB_SCHEMES`,
which exists to decide what a *cataloger's* autocomplete searches. Those are not
the same question. A library may well want catalogers to search all of LCSH,
LCGFT and Homosaurus while patrons may only propose Homosaurus terms and
free-text tags -- or the reverse.

And free-text tags cannot be turned off at all: `folk` is accepted
unconditionally, ahead of the vocab lookup.

### 3. It is env-only, so a cataloger cannot configure it

Every knob here is an environment variable read at boot. The maintainer's ask is
that **catalogers** configure it, which means a stored, editable policy and an
admin screen -- the shape `vocabsources` and `profiles` already have, not the
shape `LCATD_*` has.

## Scope

- A **suggestion policy** document in the store, not the environment:
  - `enabled` (default **off**: this is opt-in);
  - `schemes`: which controlled vocabularies patrons may propose from, a subset
    of the loaded ones;
  - `freeText`: whether `folk` tags may be proposed at all, and if so whether
    novel tags are allowed or only ones already in use.
- `GET`/`PUT /v1/config/suggestions`, admin-gated, audited (**tasks/259** is the
  filed bug that admin config changes leave no audit trail -- do not add another
  unaudited config surface).
- Enforcement in `suggest.Service.resolveTerm`, not in the handler, so the
  policy also governs any non-HTTP caller.
- The public suggestion route registers only when the policy enables it, and
  `GET /config` reports it so the discovery site can hide the affordance rather
  than offering a button that 404s.
- An admin screen to edit it.

## Design notes / open questions

- **Cataloger-configurable is not the same as admin-configurable.** The ask says
  "catalogers"; today `librarian` edits records and `admin` edits configuration.
  Which role owns this policy is a real decision. Suggest: `admin` writes it,
  `librarian` reads it -- a policy that decides what the public may submit is
  closer to a deployment setting than to a cataloging action.
- **Turning free-text off retroactively.** Existing `folk` aggregates do not
  disappear. The policy should gate *intake*, and the queue should keep showing
  what is already there so a cataloger can still promote or tombstone it. Do not
  filter the queue by the current policy: that would hide records from the
  people who must resolve them.
- **Interaction with promotions.** `suggest/promotion.go` maps a folk tag onto a
  controlled term. If the policy forbids the scheme that tag would be promoted
  into, promotion should still be allowed -- the cataloger is the authority, and
  the policy binds patrons.
- Does an allowlist need to be per-scheme-*and*-per-branch (e.g. "Homosaurus,
  but only under `homoit0000075`")? Probably not for v1; note it and move on.

## Acceptance

- A fresh deployment does not accept anonymous suggestions until the policy is
  enabled, and `GET /config` says so.
- With `schemes: ["homosaurus"]`, an LCSH suggestion is refused with a clear
  error while a Homosaurus one is accepted; a cataloger can still add either.
- With `freeText: false`, a `folk` suggestion is refused; existing folk
  aggregates still appear in the queue and can still be promoted or tombstoned.
- Every write to the policy leaves an audit entry.
