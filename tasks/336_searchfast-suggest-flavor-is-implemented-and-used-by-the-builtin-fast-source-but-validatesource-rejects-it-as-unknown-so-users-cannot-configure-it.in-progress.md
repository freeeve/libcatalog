# 336 -- searchfast suggest flavor is implemented and used by the builtin fast source but validateSource rejects it as unknown, so users cannot configure it

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

## Symptom

A librarian cannot register or edit a vocab source that uses the **searchfast** live-typeahead
flavor, even though the flavor is fully implemented and ships enabled on the builtin `fast`
source. The create/update API rejects it and the UI dropdown never offers it.

Measured on a throwaway HEAD clone:

```
GET  /v1/vocabsources           -> builtin 'fast' present, suggestFlavor="searchfast"
POST /v1/vocabsources {suggestFlavor:"suggest2",   suggestUrl:"https://id.loc.gov"}   -> 200 (accepted)
POST /v1/vocabsources {suggestFlavor:"searchfast", suggestUrl:"https://fast.oclc.org"} -> 400
     "vocabsrc: invalid source: unknown suggest flavor \"searchfast\""
```

So the create path works and the flavor is the only difference between the accepted and the
rejected request -- searchfast is singled out and called "unknown".

## Root cause

Two layers list only three of the four flavors; both were missed when searchfast landed
(tasks/132).

1. **Validation** -- `backend/vocabsrc/vocabsrc.go:283` (`validateSource`, called by `PutSource`
   at :217, the user create/update path):

   ```go
   switch src.SuggestFlavor {
   case FlavorSuggest2, FlavorWikidata, FlavorVIAF:
   default:
       return fmt.Errorf("%w: unknown suggest flavor %q", ErrValidation, src.SuggestFlavor)
   }
   ```

   `FlavorSearchFAST` is absent, so it falls to `default` and is rejected as "unknown".

2. **UI** -- `backend/ui/src/screens/VocabSources.svelte:326-328` offers only `suggest2`,
   `wikidata`, `viaf` in the `suggestFlavor` `<select>`; there is no searchfast option.

Yet searchfast is a real, dispatched flavor, not an unknown string:

- Defined: `backend/vocabsrc/suggest.go:26` -- `FlavorSearchFAST = "searchfast"` (tasks/132,
  "OCLC's searchFAST fastsuggest API").
- Dispatched at runtime: `suggest.go:87` and `:102` both have `case FlavorSearchFAST:`.
- Shipped in use: the builtin `fast` source is seeded with `SuggestFlavor: FlavorSearchFAST`
  (`vocabsrc.go:113`) and works -- builtins are seeded directly and bypass `validateSource`,
  which is why the flavor exists in production while users cannot select it.

Calling a defined, dispatched, builtin-used constant "unknown" is the tell that the two
allow-lists simply were not updated when tasks/132 added the flavor -- the same drift class
as libcat/333 (the queue Type filter that omits CONCERN). The Provenance and overlay-policy
dropdowns, by contrast, list every backend constant.

## Why it matters

searchFAST is the fastest typeahead for FAST subject headings, the vocabulary a queer catalog
leans on heavily. A deployment that wants a second FAST-backed source (a mirror, a scoped
subset, an alternate endpoint) cannot create one through the UI or the API -- the capability is
built and tested but reachable only on the one hard-coded builtin. If searchfast is instead
meant to be builtin-only, the error message is wrong (a reserved flavor is not "unknown") and
the generic dispatcher handling it for any source is misleading; either way the current state
is internally inconsistent.

## Expected

Add `FlavorSearchFAST` to both allow-lists so a user can configure a searchfast source the same
way suggest2/wikidata/viaf already work:

```go
case FlavorSuggest2, FlavorWikidata, FlavorVIAF, FlavorSearchFAST:
```
```html
<option value="searchfast">searchfast (OCLC fastsuggest)</option>
```

(Deriving both lists from one exported set of flavor constants would stop the next drift.)
If builtin-only is the intent instead, reject it with a message that says so, and don't route
user-source suggests through the searchfast case.

## Repro

```
node ~/libcat-e2e/harness/probe_searchfast_flavor.mjs   # clone: suggest2 accepted (200), searchfast -> 400 "unknown"
grep -n 'case FlavorSuggest2' ~/libcat/backend/vocabsrc/vocabsrc.go        # allow-list omits FlavorSearchFAST
grep -n 'suggestFlavor' ~/libcat/backend/ui/src/screens/VocabSources.svelte # dropdown omits searchfast
```

Retest: `t336` in harness/retest.mjs mints a searchfast source on a fromHead clone and asserts
`POST /v1/vocabsources` returns 400 "unknown suggest flavor" (STILL-BROKEN) with a suggest2
control that returns 200; it flips FIXED when searchfast is accepted.
