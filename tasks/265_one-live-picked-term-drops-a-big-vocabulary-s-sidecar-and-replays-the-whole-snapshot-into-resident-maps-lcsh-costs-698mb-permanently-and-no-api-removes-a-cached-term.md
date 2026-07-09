# 265 -- one live-picked term drops a big vocabulary's sidecar and replays the whole snapshot into resident maps: lcsh costs +698MB permanently, and no API removes a cached term

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Two features that are each correct, and that cancel each other out.

**tasks/167** built vocabulary sidecars so a big scheme serves range-fetched from
disk rather than "holding a big vocabulary as Go maps" (`vocab/sidecar_build.go:3`;
`vocabsrc/download.go:296`: *"big schemes serve range-fetched instead of as resident
maps"*).

**tasks/072** built the live-pick cache so *"a subject picked from a live source labels
forever -- across saves and restarts"* (`vocabsrc/cache.go:17-22`).

A scheme cannot have both. Picking one LCSH heading from the picker's live tab moves
513,125 terms into resident memory, for the life of the deployment.

## Symptom

Measured on a throwaway writable clone of the playground site (`:8476`, never :8481 or
:8501). RSS of the `lcatd` process, `ps -o rss=`:

```
lcsh = 513,125 terms, sidecar-backed        RSS  502 MB    <- baseline

cache a term into a scheme with NO sidecar  RSS  502 MB    (+0)   <- control
cache a term into lcgft (2,676 terms)       RSS  502 MB    (+0)   <- control
cache ONE term into lcsh                    RSS 1237 MB  (+735)

restart the process (cache file on disk)    RSS 1200 MB  (+698)   <- permanent
```

The two controls carry the argument. The first shows a `Reload` by itself costs
nothing, so the jump is not the reload. The second dirties a *small* sidecar-backed
scheme and also costs nothing, so the jump is not "any cache write" and not GC lag --
the cost tracks the size of the scheme that was dirtied.

Nothing breaks. Search stays correct throughout (`"cat"` → 20 hits; the cached term →
1 hit), and the sidecar files are still on disk -- they are bypassed, never deleted.
`GET /v1/vocabsources` still reports `lcsh: 513125 terms installed`. The only outward
sign is an `INFO` line:

```
INFO vocab: loose quads present; serving scheme from maps scheme=lcsh
```

## Root cause

`backend/vocab/vocab.go:271-277`, inside `buildSnapshot` pass 3:

```go
if len(snap.schemes[scheme]) > 0 {
    slog.Info("vocab: loose quads present; serving scheme from maps", "scheme", scheme)
    if err := parse(src); err != nil {   // <- parses the whole snapshot into maps
        return nil, err
    }
    continue                            // <- and never opens the sidecar
}
```

This upholds a real invariant, stated at `:300`: *"A scheme serves from exactly one
backend."* It exists because three of the five accessors read the sidecar **alone**:

```
vocab.go:549 Lookup       map first, then sidecar  -- merges
vocab.go:596 resolveExact map first, then sidecar  -- merges
vocab.go:671 Search       if sidecar != nil -> sidecar ONLY
vocab.go:647 Terms        if sidecar != nil -> sidecar ONLY
vocab.go:825 MatchLabel   if sidecar != nil -> sidecar ONLY
```

So when a single loose quad appears for a scheme, the loader's only way to keep
`Search` correct is to abandon the sidecar entirely and rebuild the scheme as maps.
It chooses correctness, which is right, and pays with the whole optimization.

**The trigger is an ordinary cataloger action.** `VocabPicker.svelte:130-136`:

```ts
function pick(t: Term): void {
  const sugg = liveSuggs[t.id];
  if (tab?.live && sugg) {
    cacheVocabTerm(sugg).catch(() => {});   // fire-and-forget
  }
  onselect(t);
}
```

`CacheTerm` (`vocabsrc/cache.go:23`) writes `cache/<scheme>/<hash>.nq` and reloads.
`buildSnapshot` lists the whole authorities prefix and parses every `.nq` it finds, so
that one file is a loose quad for its scheme forever after.

**The collision is the default configuration.** `lcsh`, `lcgft` and `lcshac` each carry
*both* a `SuggestFlavor` (a live tab in the picker) and a `SnapshotURL` (an installable
snapshot, hence a sidecar) -- `vocabsrc/vocabsrc.go:70-91`. Every scheme that can be
made big enough to need a sidecar is a scheme whose live tab can dirty it.

**A live pick is not the only trigger.** Anything that puts a quad for the scheme
anywhere under `data/authorities/` does it. The playground already carries
`data/authorities/ho/authorities.nq`, a hand-placed file of `<authority:homosaurus>`
headings, and homosaurus is correspondingly already served from maps at every boot:

```
INFO vocab: loose quads present; serving scheme from maps scheme=homosaurus
```

(Its snapshot is only 4,286 terms, so nobody has noticed.)

**There is no way back.** `POST /v1/vocabcache` (`vocabsources_handlers.go:101`) is the
only route that touches the cache -- there is no `DELETE`, no eviction, and no TTL.
`RemoveSnapshot` (`vocabsrc.go:340-353`) deletes the snapshot and its meta and never
looks at `cache/`. `cachedSchemes` (`cache.go:69`) then keeps the scheme in the reload
set permanently, by design. Undoing a single click requires an operator to find and
delete a sha256-named blob by hand.

Read, not measured: pass 3's dirtiness test at `:271` runs *before* the non-heading
debris guard at `:313-321`, which deletes terms carrying no labels at all ("a merge
marker on an absent node, a legacy `authority:aliases` tagAlias statement"). So a
scheme whose only loose quads are debris the loader itself is about to discard still
loses its sidecar for them.

## Why it matters

The sidecar exists for exactly one deployment: the one with a big vocabulary and a
small machine. That is the deployment this breaks, and it breaks it at the worst
moment.

**The failure is decoupled from the action that causes it.** The cache file is read at
*boot*. A container sized against a 502 MB baseline -- say a 1 GB limit -- keeps
running happily after a cataloger picks an LCSH heading, because the process has
already paid for its maps by then and RSS creeps rather than spikes. It dies on the
next restart, needing 1.2 GB to warm an index it used to warm in half that. The
operator gets an OOM kill at deploy time, days later, with nothing connecting it to a
subject heading somebody chose on Tuesday. `GET /v1/vocabsources` reports the same
`513125 terms installed` before and after; the only trace is an `INFO` log line among
the boot chatter.

**The cost is not opt-in and not visible.** The cataloger picking a heading has no idea
the pick is anything but a pick -- `cacheVocabTerm` is fire-and-forget, and it is right
to be, since the cache is what makes the subject label forever. Nothing in the UI, the
API, or the source listing says a scheme has fallen back to maps.

**It compounds.** All three of `lcsh` / `lcgft` / `lcshac` can be dirtied independently,
each costing its own snapshot's worth of resident memory. And the fallback is sticky in
the worst direction: once `cache/lcsh/` is non-empty, no sequence of API calls restores
the sidecar, including uninstalling and reinstalling the snapshot.

Measured against the real playground: `cache/lcsh/` is empty today, so lcsh is still
sidecar-backed -- but `cache/lcnaf/` holds 1 file and `cache/wikidata/` holds 2. The
catalogers are already using the live tabs. Those two schemes have no snapshot
installed, so no sidecar, so no cost. The same click on the neighbouring lcsh tab costs
698 MB. The bomb is armed, not defused.

Nothing is corrupted. What is lost is the entire benefit of tasks/167, to whichever
scheme a cataloger happens to touch first.

## Expected

The invariant at `:300` is doing the work here, and it is the thing to relax. Two of
the five accessors already merge the map path with the sidecar and are correct as
written -- the other three need the same treatment, and then no scheme ever has to
choose a single backend:

- **`Search` (`:671`)**: merge two label-ordered streams (the sidecar's and
  `snap.search[scheme]`'s) rather than returning the sidecar's alone. Both are already
  sorted by `normLabel`; this is a k-way merge with a `limit`, no extra memory.
- **`Terms` (`:647`)**: concatenate `sc.all()` with the map's terms, then sort -- the
  function already sorts.
- **`MatchLabel` (`:825`)**: union the two exact-label match sets.
- Then delete the dirty-scheme replay at `:271-277` and the shared-source cleanup at
  `:302-307`, and let a scheme carry a sidecar *and* an overlay. The overlay is tiny by
  construction: it is the set of live picks, not a vocabulary.

A cheaper stopgap, if the merge is too large a change to take now: at minimum move the
dirtiness test to *after* the debris guard so label-less bookkeeping stops disqualifying
a scheme, and fold `BuildSidecar` into `CacheTerm` so a pick rebuilds the affected
scheme's sidecar instead of poisoning it. `CacheTerm` already pays for a full
`Reload`, so the marginal cost is one sidecar build on a scheme that just changed.

Independently of the fix:

- **Give operators an undo.** A `DELETE /v1/vocabcache?scheme=&id=`, and have
  `RemoveSnapshot` sweep `cache/<scheme>/` (compare **252**, where `RemoveSnapshot`
  also leaves its sidecar artifacts behind, and its own doc comment claims otherwise).
- **Say it louder than `INFO`.** A scheme falling back to maps is a capacity event.
  `slog.Warn` with the term count -- *"lcsh: 513125 terms now resident (loose quads
  present)"* -- turns an unexplained OOM into a grep.
- Surface it on `GET /v1/vocabsources`: a `residentTerms` or `sidecar: false` field
  next to `installed.terms` costs nothing and answers "why is this process 1.2 GB".

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_vocab_sidecar_bypass.mjs   # M5, M7
cd ~/libcat-e2e && node harness/retest.mjs                       # check t265
```

The probe never addresses :8481 or :8501. It builds `backend/cmd/lcatd`, makes an APFS
clone (`cp -Rc`, copy-on-write) of the playground's site, boots a writable instance on
:8476, measures RSS around each cache write, reboots to show the cost survives, and
deletes the clone. `M3` (dirty a scheme with no sidecar) and `M4` (dirty a 2,676-term
sidecar-backed scheme) are the controls that rule out "the reload did it" and "GC lag
did it"; `M6` shows search stays correct; `M9` shows the sidecar files are still there.

`harness/probe_vocab_cache.mjs` is the companion negative result: it measures that a
cached term *is* findable on a sidecar-backed scheme (10/10), i.e. the replay really
does keep `Search` correct. That probe is why this task asks for a merge rather than
reporting a search bug.

By hand, against a throwaway instance with an lcsh snapshot installed:

```bash
go build -o /tmp/lcatd-rw ./backend/cmd/lcatd
cp -Rc ~/libcat-playground/site /tmp/site-rw
LCATD_LISTEN_ADDR=:8476 LCATD_BLOB_DIR=/tmp/site-rw LCATD_LOCAL_AUTH=1 \
  LCATD_BOOTSTRAP_ADMIN="ro@example.org:changeme123" \
  LCATD_ABUSE_SECRET=0123456789abcdef0123456789abcdef /tmp/lcatd-rw &
PID=$!
ps -o rss= -p $PID          # ~502 MB, lcsh served from its sidecar

TOK=$(curl -s -XPOST -H 'Content-Type: application/json' \
  -d '{"email":"ro@example.org","password":"changeme123"}' localhost:8476/v1/auth/login | jq -r .accessToken)

curl -s -XPOST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
  -d '{"source":"lcsh","scheme":"lcsh","id":"https://id.loc.gov/authorities/subjects/sh85001234","label":"Aardvarks"}' \
  localhost:8476/v1/vocabcache
# {"cached":true}

sleep 3; ps -o rss= -p $PID # ~1237 MB
# and the log: INFO vocab: loose quads present; serving scheme from maps scheme=lcsh

kill $PID; /tmp/lcatd-rw &  # restart against the same site
ps -o rss= -p $!            # ~1200 MB -- the cache file is on disk; there is no undo

chmod -R u+w /tmp/site-rw && rm -rf /tmp/site-rw
```

Equivalently, through the UI: open any work, add a subject, choose the **lcsh** live
tab in the picker, and click one heading.
