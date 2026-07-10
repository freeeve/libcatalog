# 323 -- POST /v1/works/split is not idempotent: a retried or double-clicked split leaves two contradictory workAssignment pins, and the winner is decided by which minted work id sorts higher

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

Related but distinct from `214`, which is about split/merge accepting ids **the
grain never describes** (phantom instances, ghost sources). This one is about a
perfectly legitimate split of an instance the source work really owns, performed
twice.

## Symptom

`POST /v1/works/split` mints a new work id on **every** call. Call it twice for
the same instance and the source grain ends up asserting that one instance
belongs to two different works. Nothing rejects the second call, nothing removes
the first pin, and nothing reconciles them.

Measured on a throwaway writable clone built from `HEAD` (`node
harness/probe_split.mjs`, 2026-07-10). The probe mints its own sentinel work
through copycat, so no pre-existing record is involved:

| # | Check | Result |
|---|---|---|
| S0 | control: sentinel minted | work `wgh4pkrmp6jijs`, instance `id11goiqvnd0i4` |
| S1 | control: first split | `200`, minted `wctj9bve7uv9ec` |
| S2 | control: markers really landed | grain carries `lcat:splitFrom` + `lcat:workAssignment` -> `wctj9bve7uv9ec` |
| **P1** | **second split of the same instance** | **`200`**, minted `wq137b1nfvq8dk` |
| **P1** | **resulting grain** | **2 contradictory `lcat:workAssignment` pins for one instance, 2 `lcat:splitFrom` markers** |

`C3`/`C4` in that probe measure the *deferred-to-ingest* design (the new work
does not resolve until the next ingest; the instance still lists under the
source). Those are documented behaviour and are printed as context, not judged.

### The winner is a coin flip

An 8-trial experiment against the same clone, each trial minting a fresh
sentinel and splitting the same instance twice:

- **8/8**: the surviving pin was the **lexically greater** work IRI.
- **4 trials** the first split won; **4 trials** the second split won.

Work ids are random. So which of two splits takes effect is decided by
`sort(idA, idB)`, not by which split the cataloger performed later.

## Root cause

`backend/httpapi/records_handlers.go:333` mints before it marks:

```go
newWork := identity.Mint(identity.WorkPrefix)   // <-- fresh id on every call
... bibframe.AddSplitMarkers(grain, req.From, newWork, req.Instances)
```

`bibframe/merge.go:139 AddSplitMarkers` carries the comment *"Adding a marker
that already exists is a no-op, so it is idempotent."* That is true **of the
function**. The handler defeats it: because a new `newWork` is minted before
each call, no two calls ever present the same marker, so the no-op branch is
unreachable and the endpoint is not idempotent at all.

Both pins then survive into the resolver, and the last one seen wins:

- `bibframe/reingest.go:80 scanPinsDataset` walks the stored **canonical**
  (sorted) grain in quad order.
- `ingest/ingest.go:87-88` calls `SeedPin(p.Instance, p.Work)` for each.
- `identity/resolver.go:114`:
  ```go
  func (r *Resolver) SeedPin(instanceID, workID string) {
      r.pinByInst[instanceID] = workID   // map[string]string -- last write wins
      r.usedWork[workID] = true
  }
  ```

Two consequences fall out of those two lines:

1. `pinByInst` is a plain map, so the pin that survives is whichever
   `lcat:workAssignment` quad sorts later after `ds.Canonical()` -- i.e. the
   greater work IRI. That matches the 8/8 result above.
2. `usedWork[workID] = true` runs for **both** ids, so the losing work id is
   permanently reserved and can never be minted again, while denoting nothing.
   Every retried split burns an id out of the space.

The UI invites the retry:

- `backend/ui/src/screens/WorkEditor.svelte:92 doSplit()` sets no busy flag, so
  the button stays live for the duration of the request.
- `:284` keeps the instance's `select for split` checkbox rendered after a
  successful split (correctly -- the instance really is still on the work until
  ingest), so the screen a cataloger sees after splitting is indistinguishable
  from the screen before.

## Why it matters

This is the same shape as `115`, `261`, `300`, `305` and `313`: **the durable
record of an intention is written before the intention is carried out, and
nothing reconciles them.**

Concretely, a cataloger who splits an instance, sees the request time out or
double-clicks, and splits again has a 50% chance that their *second, corrected*
split is silently discarded at the next ingest in favour of the first. No error,
no warning, no diff -- the editor reports `split recorded` both times
(`WorkEditor.svelte:100`). The grain asserts both, and the resolver quietly picks
one by sort order.

Multi-instance works are not hypothetical: **5 of 40** works sampled from the
queerbooks catalogue (`:8501`, read-only) carry 2 instances. Splitting them is
exactly what the feature is for.

## Expected

Any one of:

- The endpoint refuses a split of an instance that already carries a
  `lcat:workAssignment` pin (`409`), unless the caller passes an explicit
  re-pin/override.
- Or it is made genuinely idempotent: look for an existing pin for
  `req.Instances` in the source grain, and reuse that work id rather than
  minting a fresh one. `AddSplitMarkers`'s no-op branch then does the job its
  comment already promises.
- Or the last split wins deterministically -- but that requires an ordering
  signal in the grain (a timestamp on the marker), because IRI sort order
  carries none.

Whichever is chosen, `SeedPin` should not reserve a work id it is about to
discard, and `pinByInst` should not silently drop a conflicting pin -- a
conflicting pin is a data-integrity event and belongs in the ingest report.

## Repro

```
cd ~/libcat-e2e && node harness/probe_split.mjs      # 3/4 -- P1 fails
cd ~/libcat-e2e && node harness/retest.mjs           # check t323
```

Both boot a throwaway writable clone (port 8459 / 8458) from `git archive HEAD`,
mint a sentinel work via copycat, split one instance twice, and read the source
grain back through `GET /v1/works/{id}` -> `.nquads`. Nothing is written to the
playground; the clone is deleted afterwards.
