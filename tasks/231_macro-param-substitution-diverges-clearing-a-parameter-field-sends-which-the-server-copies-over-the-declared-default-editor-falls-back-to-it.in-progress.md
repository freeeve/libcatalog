# 231 -- macro param substitution diverges: clearing a parameter field sends "", which the server copies over the declared default (editor falls back to it)

Filed from libcat on 2026-07-09 (cross-repo ask).

## Symptom

A macro with a parameter that declares a default. In the Batch screen, type a
value into the parameter field, change your mind, clear it, and run. Every record
in the selection fails.

Measured on the 8481 playground (`ui/probe_macro_params.mjs`), macro
`{params: [{name: "tag", default: "zz-e2e-default"}], ops: [add work.tags "${tag}"]}`:

```
PASS M1  editor: a blank parameter falls back to the default   applyParams({tag:""}) -> "zz-e2e-default"
PASS M2  server: an omitted parameter uses the default         applied=2 failed=0
PASS M3  server: an explicit parameter is substituted          applied=2 failed=0
FAIL M4  server: a blank parameter falls back to the default   applied=0 failed=2
                                                               "editor: op 0 (add work.tags): empty value"

PASS M5  the blank field advertises the default                placeholder reads "zz-e2e-default"
PASS M6  CONTROL: an untouched field is omitted, default applies
                                                               POST body params={}; {"matched":2,"applied":2,"failed":0}
FAIL M7  a cleared field falls back to the default             after typing then clearing:
                                                               POST body params={"tag":""}
FAIL M8  the cleared-field run applies the default             {"matched":2,"applied":0,"failed":2}
                                                               per-work error "editor: op 0 (add work.tags): empty value"
```

M6 is the control: a field the cataloger never touches is omitted from the
request entirely, and the default applies. Only a *cleared* field sends `""`.

The same macro replayed in the work editor with the same blank value applies the
default and stages the edit.

## Root cause

The client skips empty values when building its lookup table; the server does
not.

`backend/ui/src/lib/macros.ts:22`
```js
for (const [name, v] of Object.entries(values)) {
  if (v !== "") lookup[name] = v;     // "" never overrides the default
}
```

`backend/batch/macros.go:115`
```go
maps.Copy(lookup, values)             // "" overrides the default
```

`lib/macros.ts:1-3` states the invariant that is broken:

```
// Client-side macro replay: ${name} parameter substitution over an op list,
// mirroring the server's batch.ApplyParams so a macro means the same thing
// replayed in the editor or run over a selection (tasks/047).
```

Substitution then yields `v: ""`, and `editor/apply.go:368` (`validateValue`)
rejects it with `empty value` for every work in the selection. Nothing writes an
empty value -- `add` and `set` both validate, and `remove` cannot match `""` --
so this is a failed run, not corruption.

## Why it matters

The parameter input's placeholder is the declared default
(`BatchOps.svelte:290`), which tells the cataloger that blank means "use the
default". The editor honours that reading. The batch screen honours it only if
the field was never focused; clear it and the run fails on every record.

The failure is opaque. The per-work error is `editor: op 0 (add work.tags):
empty value` -- it never mentions the parameter, the macro, or the default. On a
selection of one work that is a puzzle; on a few thousand it is a wall of
identical errors with no cause named. And because a dry run reproduces it
faithfully, the cataloger's next move is to go hunting through the *records* for
the problem, which is not where it is.

The parity claim also matters on its own: a macro is supposed to mean the same
thing in both places, and a saved macro is exactly the artifact a cataloger
reuses across the two.

## Expected

- `batch/macros.go:115` skips empty values, matching the client:
  ```go
  for k, v := range values {
      if v != "" {
          lookup[k] = v
      }
  }
  ```
  Blank then means "use the default" everywhere, which is what the placeholder
  already promises.
- If instead an explicit empty value is meant to be legal and distinct from
  "unset", then the client must stop swallowing it, `BatchOps.svelte` must stop
  advertising the default as a placeholder, and the resulting error should name
  the parameter rather than the op.
- Either way, when substitution produces a value the editor will reject, the
  error should say which parameter produced it. `applyParams` knows the name.
- A shared table test over `ApplyParams` / `applyParams` with the same fixtures
  would have caught this; the two implementations are tested separately today.

## Repro

```
cd ~/libcat-e2e && node ui/probe_macro_params.mjs
```

Expect `M4`, `M7` and `M8` to flip to PASS, with `M6` (the control) still
passing. The probe creates one sentinel macro and deletes it; every batch call is
a `dryRun`, so no work is ever written. `harness/retest.mjs` carries the same
check as `t231`.

## Outcome

Fixed in v0.80.0 (commit 0099786), taking the first Expected option
plus the wire-shape alignment: `batch.ApplyParams` skips blank values
exactly like `lib/macros.ts` (blank means "use the default"
everywhere, as the placeholder promises), a blank parameter with no
default fails closed naming the parameter (the existing "has no value"
path now covers it, satisfying the name-the-parameter ask), and
BatchOps omits cleared fields from the POST so an untouched and a
cleared field produce the same request. Mirrored table cases pinned in
batch_test.go and macros.test.ts per the shared-fixture suggestion.

Verified with the filer's probe_macro_params.mjs against the rebuilt
8481: 10/10, M4/M7/M8 flipped with M6 still green.
