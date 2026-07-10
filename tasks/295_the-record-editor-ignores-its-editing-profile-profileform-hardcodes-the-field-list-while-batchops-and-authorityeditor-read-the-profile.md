# 295 -- the record editor ignores its editing profile: ProfileForm hardcodes the field list while BatchOps and AuthorityEditor read the profile

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

`backend/profiles/profiles.go:1-7` says what an editing profile is for:

> *"Package profiles defines editing profiles -- the JSON documents that **replace MARC
> frameworks** (Koha's tag/subfield configuration) for the BIBFRAME-native editor. **A
> profile declares which fields a form shows**, their cardinality, datatype, value source,
> and defaults; the framework ships conservative defaults and a deployment overrides or adds
> its own as git-reviewed JSON."*

The form does not read it.

```svelte
// The shipped work-monograph and instance-ebook shapes (profiles/defaults).   ProfileForm.svelte:51
const WORK_FIELDS: FieldSpec[] = [ { path: "title", label: "Title", kind: "single" }, … ];   // :55
const INSTANCE_FIELDS: FieldSpec[] = [ … ];                                                 // :70
…
const specs = kind === "work" ? WORK_FIELDS : INSTANCE_FIELDS;                              // :124
```

Two hand-maintained literals, chosen by a `kind` string. `ProfileForm.svelte` -- the
component named after the feature -- fetches no profile, and `WorkEditor.svelte` imports no
profile API. It does, however, print the profile's name above the form it does not obey:

```svelte
<span class="muted meta">profile {doc.profileId}</span>                        WorkEditor.svelte:157
```

So an admin overrides `work-monograph`, the server validates it (`profilesvc`), stores it
under an etag, audits `PROFILE_EDIT` (`profiles_handlers.go:72`), the Profiles screen shows
`overridden: true` -- **and every cataloger's editor renders exactly what it rendered
before.**

## Symptom

Measured on the running playground (`:8481`). The shipped `work-monograph` was overridden
with two changes -- `title.label` renamed, `tags.hidden = true` -- then reverted.

**The batch op builder obeys the override:**

```
before  ["Title", "Subtitle", "Contributors", "Summary", …, "Tags", …]
after   ["zz-e2e Renamed Title", "Subtitle", "Contributors", "Summary", …]      <- Tags gone
```

**The record editor, same override, same reload, does not:**

```
after   ["Title", "Subtitle", "Contributors", "Summary", "Language",
         "Subject headings", "Subjects", "Tags", "Genre / form", …]
```

Still `"Title"`. Still a `Tags` field, marked `hidden: true` in the profile the page names
one line above the form. Both endpoints serve the override correctly -- this is not a server
bug:

```
GET /v1/profiles/work-monograph  -> isDefault=false, title label "zz-e2e Renamed Title", tags hidden=true
GET /v1/profiles                 -> overridden=true, same field values
DELETE /v1/profiles/work-monograph -> 204, shipped default restored
```

## Root cause

`backend/ui/src/components/ProfileForm.svelte:124`:

```svelte
const specs = kind === "work" ? WORK_FIELDS : INSTANCE_FIELDS;
```

`kind` is a literal passed by the two mount sites, `WorkEditor.svelte:257` (`kind="work"`)
and `:287` (`kind="instance"`). There is no `profileId`, no `fetchProfile`, no `fields`
prop. `WORK_FIELDS` (`:55`) and `INSTANCE_FIELDS` (`:70`) are transcriptions of
`profiles/defaults/work-monograph.json` and `instance-ebook.json`, and the comment above
them (`:51`) says so.

The transcription is nearly faithful, which is why nothing has caught it: both carry the same
eleven paths. **They already disagree on order**, and a profile's field order is its display
order:

```
work-monograph.json   … language, subjects, subjectLabels, tags …
WORK_FIELDS           … language, subjectLabels, subjects, tags …
```

The drift has begun and nothing noticed, because **the two copies are only ever compared by
eye.**

## This is a gap, not a design decision

Two neighbours on the same profile mechanism read the profile at runtime.

**`BatchOps.svelte:93`** derives its op-builder field list from the live profile, honouring
`label` and filtering on `hidden`:

```svelte
const editableFields = $derived((workProfile?.fields ?? []).filter((f) => !f.hidden));   // :93
…
fetchProfiles().then((r) => (workProfile = r.profiles["work-monograph"] ?? null), () => {});   // :96-97
```

**`AuthorityEditor.svelte`** takes its labels and help text from `authority-topic`, and its
header comment states the intent outright:

```svelte
// Field labels and help come from the authority-topic profile -- the same
// profile mechanism records use.                                        AuthorityEditor.svelte:4-6
…
function fieldLabel(path, fallback) { return profile?.fields.find((f) => f.path === path)?.label ?? fallback; }  // :194
function fieldHelp(path)            { return profile?.fields.find((f) => f.path === path)?.help ?? ""; }         // :198
```

*"the same profile mechanism records use"* is the claim under test, and it is false. The
authority editor is profile-driven; the record editor is not.

The rest of the `Field` struct confirms how much is going unread. `Help`, `MarcHint`
(*"names the roughly-equivalent MARC field for copy catalogers"*, `profiles.go:94`),
`Default`, `Min`/`Max`, and `ValueSource` are declared, validated, and shipped. Outside
`AuthorityEditor`'s `help` and `Duplicates.svelte`'s cardinality read, **no record-editing
surface consumes any of them**; `grep -rn marcHint backend/ui/src --include=*.svelte`
returns nothing.

## Why it matters

**The feature's headline promise is the one thing it does not do.** "Replaces MARC
frameworks" means a deployment can decide which fields its catalogers see. A library can
edit `work-monograph`, get a `200`, an etag, an audit row, and an `overridden: true` badge
-- and no cataloger's screen changes. There is no error anywhere to notice.

**The override silently half-lands.** It *does* reshape the batch op builder. So a
deployment that hides a field gets a catalog where the field cannot be batch-edited but can
still be typed into the record editor, one field at a time. That is worse than the override
doing nothing, because the two surfaces now disagree about what the library's schema is.

**`profile {doc.profileId}` is a lie in the UI.** It tells the cataloger which framework
governs the form in front of them. Nothing governs it.

**The duplication has already drifted.** `WORK_FIELDS` is a hand-copy of a JSON file in
another language in another directory, with no test comparing them, and the two lists order
`subjects` and `subjectLabels` differently today. The first person to *add* a field to
`work-monograph.json` and not to `ProfileForm.svelte` will ship a field that exists in the
batch builder, in validation, and in the merge tool's cardinality logic, and nowhere a
cataloger can enter it.

## Expected

Pick one, and make the package doc and `AuthorityEditor.svelte:4-6` true:

- **Drive the form from the profile.** `WorkEditor` already knows `doc.profileId`. Fetch it,
  pass `fields` into `ProfileForm`, and take `label`, `help`, `hidden`, `readOnly`,
  `marcHint`, and order from the server. The presentation concerns the server has no
  vocabulary for -- `kind`, `options`, `decode`, `section`, `wide` -- can stay in a local
  table **keyed by path**, merged onto the profile's field list. That keeps `SearchSelect`
  and `bisacTerm` where they belong while the *set of fields and their labels* becomes the
  deployment's to choose. `BatchOps.svelte:93` is the working precedent, thirty lines long.

  Note `BatchOps.svelte:97` hardcodes `r.profiles["work-monograph"]` regardless of the
  record's own `profileId`, so a work on `fastadd` already gets the wrong field list there.
  Same fix, same place.

- **Or say plainly that the record editor's shape is compiled in.** Then `profiles.go:1-7`
  should not claim a profile "declares which fields a form shows", `AuthorityEditor.svelte:5`
  should not say records use the same mechanism, and `WorkEditor.svelte:157` should not name
  a profile it ignores. `instance-ebook.json` would be a file no code reads.

- **Either way, add a test that the two field lists agree.** A Vitest that imports
  `WORK_FIELDS` and asserts its paths equal `work-monograph.json`'s -- **in order** -- would
  have caught the `subjects`/`subjectLabels` swap already present, and pins the copies
  together until one of them is deleted.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_profile_drives_editor.mjs   # C3, C3b
cd ~/libcat-e2e && node harness/retest.mjs                        # check t295
```

Read/write against the playground on `:8481`. The probe overrides the **shipped**
`work-monograph` -- the one profile `probe_profiles.mjs` deliberately refuses to touch,
because an override of it "would reshape every cataloger's editor". That is the claim under
test. It reverts in a `finally` and asserts the shipped default is back (`C4`); it stages no
ops and saves no work.

Its controls carry the argument. `C0` shows we started from the shipped default. `C1` shows
the editor and the op builder **agree** before the override, so the disagreement after it is
caused by the override. **`C2` is the one that matters: after the PUT, the op builder renames
its Title option and drops its Tags option.** Without `C2`, "the editor did not change" is
indistinguishable from "the PUT never landed" -- and on the first run of this probe it was
exactly that. `C2` failed because `page.goto()` to a URL differing only in its hash is a
same-document navigation, so the Svelte component never remounted and its `onMount`
`fetchProfiles()` never re-ran. The control caught a harness bug that would otherwise have
been filed as a server bug.

By hand:

```bash
TOK=...   # an admin token
curl -s -H "Authorization: Bearer $TOK" localhost:8481/v1/profiles/work-monograph > /tmp/wm.json
jq '.profile | (.fields[] | select(.path=="tags") | .hidden) = true' /tmp/wm.json > /tmp/wm2.json
curl -s -XPUT -H "Authorization: Bearer $TOK" -H 'content-type: application/json' \
  -d @/tmp/wm2.json localhost:8481/v1/profiles/work-monograph          # 200 + etag
# open #/batch      -> the Tags option is gone from the Field select
# open #/works/<id> -> the Tags field is still there
curl -s -XDELETE -H "Authorization: Bearer $TOK" localhost:8481/v1/profiles/work-monograph   # 204, revert
```

The `DELETE` matters: leaving the override in place is the only way this probe can damage the
playground.
