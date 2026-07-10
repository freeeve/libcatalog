# 337 -- nine admin screens leak raw pkg-prefixed backend error strings; humanApiMessage from 197 was applied to only exports/batch/attachments

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

## Symptom

On nine admin screens, a failed operation renders the backend's internal error string
verbatim -- including its Go package prefix. A librarian who registers a vocab source with a
bad URL sees, in the screen's error banner:

```
vocabsrc: invalid source: urls must be http(s)
```

not a cataloger-facing "Urls must be http(s)". The same leak shows on the copycat screen
(`copycat: invalid request: ...`) and every other screen listed below. This is exactly the
class tasks/197 and tasks/272 fixed -- but only on the three surfaces those tasks touched.

Measured: `POST /v1/vocabsources {name,scheme,snapshotUrl:"notaurl"}` returns
`400 {"error":"vocabsrc: invalid source: urls must be http(s)"}` (confirmed on a clone), and
`VocabSources.svelte:181` assigns that string straight to the error banner.

## Root cause

`humanApiMessage(e, fallback)` (`backend/ui/src/lib/api.ts:74`, added by tasks/197) exists
precisely to strip these prefixes: `e.message.replace(/^[a-z]+: (invalid request: )?/, "")`.
Its own doc says *'service-internal prefixes ("batch: invalid request:") never escape to the
UI'*. But only three files call it:

- `screens/BatchOps.svelte`, `screens/Exports.svelte`, `components/AttachmentsPanel.svelte`.

Every other error-surfacing screen assigns the raw `ApiError.message` (which `api.ts:116` sets
to the response's `error` field verbatim):

| screen | lines |
|---|---|
| `screens/VocabSources.svelte` | 98, 111, 127, 163, 181, 195 |
| `screens/CopyCat.svelte` | 160, 181, 194, 233, 256, 299, 324 |
| `screens/Profiles.svelte` | 56, 78, 101, 130 |
| `screens/Macros.svelte` | 152 |
| `screens/Withdrawals.svelte` | 46, 62 |
| `screens/Promotions.svelte` | 120, 135 (interpolated into `decide failed: <raw>`) |
| `screens/Duplicates.svelte` | 79 |
| `screens/AuthorityEditor.svelte` | 134, 187 |
| `screens/Audit.svelte` | 52 (interpolated into `audit load failed: <raw>`) |

The sentinel errors that leak are `batch.ErrValidation` = `"batch: invalid request"`
(batch.go:41), `vocabsrc.ErrValidation` = `"vocabsrc: invalid source"` (vocabsrc.go:29), and
`copycat.ErrValidation` = `"copycat: invalid request"` (copycat.go:70) -- each handler passes
`err.Error()` straight to `writeError` (e.g. `writeVocabSrcError`, `writeBatchError`), so the
package prefix rides through to the `error` field and out to the UI. Same "the fix was applied
to the paths that had the bug, not to the class" shape as libcat/333 and libcat/336.

## Why it matters

Eve asked for look-and-feel / UX findings. A librarian is shown Go package names
(`vocabsrc:`, `copycat:`, `batch:`) in an error toast on nine of the twelve screens that can
error -- the same defect 197 and 272 already judged worth fixing, just on different screens.
It is inconsistent (exports humanizes, vocabularies does not) and it exposes internal naming.

## Expected

Route every screen's error copy through `humanApiMessage(e, fallback)`, the helper 197 already
built for this. E.g. `VocabSources.svelte:181`:

```diff
-      error = e instanceof ApiError ? e.message : "registering the source failed";
+      error = humanApiMessage(e, "registering the source failed");
```

(A lint or a shared `catch` wrapper would stop the next screen from re-introducing it.) Note
the helper's regex only strips `invalid request:` after the package word, so `vocabsrc:
invalid source:` becomes `Invalid source:` -- still prefix-free of the package name; widen the
regex to `(invalid (request|source): )?` if the leading "Invalid source:" is also unwanted.

## Repro

```
node ~/libcat-e2e/harness/probe_error_copy.mjs   # clone: POST a bad-URL source, assert the 400 error field carries the raw "vocabsrc:" prefix
grep -n humanApiMessage ~/libcat/backend/ui/src/screens/VocabSources.svelte   # -> absent
grep -rn "e.message" ~/libcat/backend/ui/src/screens/                          # -> 9 screens
```

Retest: `t337` drives the Vocabularies screen on :8481, opens the register form, submits a
source with a non-http snapshot URL, and reads the rendered error banner -- STILL-BROKEN while
it begins with a `word:` package prefix (`vocabsrc:`), FIXED once humanized. The bad URL is
rejected 400, so nothing is created and no cleanup is needed.

## Outcome -- shipped in libcat v0.147.1 (patch)

Every error-surfacing admin screen now routes its banner copy through
`humanApiMessage(e, fallback)` instead of assigning the raw `ApiError.message`.
`humanApiMessage`'s regex was widened to `^[a-z]+: (invalid (request|source): )?`
so `vocabsrc: invalid source: urls must be http(s)` fully strips to
`Urls must be http(s)` (not the half-stripped `Invalid source:`).

**Fixed twelve screens, not the nine reported.** The report listed nine; a new
source-scan test (below) surfaced three more with the same leak that the manual
census missed -- `NewRecord.svelte`, `Queue.svelte`, `WorkEditor.svelte`
(clone/split). Fixing the class, not the enumerated instances, is the whole
point. Screens with simple `e instanceof ApiError ? e.message : "..."` became
`humanApiMessage(e, "...")`; interpolated ones (`decide failed: <raw>`,
`audit load failed: <raw>`, Queue's `apply/publish/folk update failed`) keep
their context wrapper but humanize the interpolated part. Five screens no longer
reference `ApiError` at all and dropped it from their imports.

**Anti-drift guard.** `api.test.ts` now has a `describe` that globs every
`/src/screens/*.svelte` and fails if any line pairs `instanceof ApiError` with a
bare `e.message` -- so the next screen added with a raw banner fails CI instead of
shipping the leak. Plus unit tests pinning the widened `humanApiMessage`
(package-prefix strip, `invalid source:` strip, bare-prefix strip, empty-strip
fallback).

- Backend is unchanged: the sentinel prefixes (`batch.ErrValidation`,
  `vocabsrc.ErrValidation`, `copycat.ErrValidation`) are internal by design and
  still ride the `error` field; the UI humanizes at the edge, which is where 197
  put the helper.

**Verified live on :8481** (Playwright): opened the Vocabularies register form,
submitted `snapshotUrl: "notaurl"`, and the banner reads `Urls must be http(s)`
with no `vocabsrc:` prefix. The backend still returns
`400 {"error":"vocabsrc: invalid source: urls must be http(s)"}` (unchanged).
UI suite 347/347, svelte-check clean.
