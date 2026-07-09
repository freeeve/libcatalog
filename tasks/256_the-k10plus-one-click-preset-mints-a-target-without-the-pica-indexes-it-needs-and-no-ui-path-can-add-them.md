# 256 -- the k10plus one-click preset mints a target without the PICA indexes it needs, and no UI path can add them

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Found while exercising copycat search-target management (ADMIN_FEATURES C8). The
target *store* is healthy; `Target.Indexes` reaches the wire exactly as designed.
The defect is that the UI's one-click preset for K10plus builds a target the
server's own `DefaultTargets` says is misconfigured -- and then offers no way to
correct it.

## Symptom

`DefaultTargets` seeds `k10plus-sru` with the PICA identifier indexes, and its
comment (`copycat/targets.go:37-39`) says why:

> LOC speaks the Bath-profile identifier indexes as-is; DNB only answers SRU 1.1
> and names its schema MARC21-xml, with dnb.num covering both standard numbers;
> **K10plus wants its PICA identifier indexes.**

The Suggested-targets row on `#/copycat` offers a **`k10plus`** button pointing at
the very same URL. Clicking it mints a target with no indexes at all:

```
seeded  k10plus-sru : {"url":"https://sru.k10plus.de/opac-de-627","protocol":"sru",
                       "indexes":{"isbn":"pica.isb","issn":"pica.iss"}}
minted  k10plus     : {"url":"https://sru.k10plus.de/opac-de-627","protocol":"sru"}
```

Both are then searched, and they emit different CQL for the same ISBN. Measured on
:8481 by pointing two sentinel targets at a local SRU stub that echoes back the
`query` param it receives -- i.e. this is the CQL libcat actually put on the wire,
not a reading of the source:

```
target WITH  indexes {"isbn":"pica.isb","issn":"pica.iss"}
target PLAIN indexes (none)

POST /v1/copycat/search  fields=[{index:"isbn", term:"9780306406157"}]
  WITH  -> pica.isb = "9780306406157"
  PLAIN -> bath.isbn = "9780306406157"

POST /v1/copycat/search  fields=[{index:"issn", term:"0378-5955"}]
  WITH  -> pica.iss = "0378-5955"
  PLAIN -> bath.issn = "0378-5955"

control, an access point neither target overrides:
  WITH  -> dc.title = "zz"
  PLAIN -> dc.title = "zz"      <- identical, so the divergence above is the override
```

So an ISBN search against the one-click `k10plus` goes out as `bath.isbn`, the
index `DefaultTargets` says that server does not speak.

And there is no way to fix it from the UI. The Add-target form exposes three
controls; none is an index map:

```
details "Search targets" form fields:
  ["Target name", "Target URL", "Protocol"]     <- no version, no schema, no indexes
```

## Root cause

Three pieces.

**1. The preset data carries no knobs.** `backend/ui/src/screens/CopyCat.svelte:42-47`:

```ts
const SUGGESTED_TARGETS: (CopycatTarget & { blurb: string })[] = [
  { name: "loc",            url: "lx2.loc.gov:210/LCDB",              protocol: "z3950", blurb: "…" },
  { name: "loc-sru",        url: "http://lx2.loc.gov:210/LCDB",       protocol: "sru",   blurb: "…" },
  { name: "k10plus",        url: "https://sru.k10plus.de/opac-de-627", protocol: "sru",  blurb: "…" },
  { name: "indexdata-test", url: "z3950.indexdata.com:210/marc",      protocol: "z3950", blurb: "…" },
];
```

The `k10plus` entry is `DefaultTargets`' `k10plus-sru` minus its `indexes`. (`dnb-sru`
has no preset at all, so its `version`/`schema` are not at risk this way. The two
`loc` presets need no overrides -- LOC speaks Bath as-is -- and Z39.50 never consults
`Indexes`, so `k10plus` is the only preset affected.)

**2. `addSuggested` would drop them even if they were there.** `CopyCat.svelte:187-195`:

```ts
await putCopycatTarget({ name: s.name, url: s.url, protocol: s.protocol });
```

`version`, `schema` and `indexes` are never forwarded. Today that is a no-op given
(1), but it means fixing the table alone is not enough.

**3. Nothing else can set them.** `CopyCat.svelte:399-406` is the whole add form --
name, url, a two-value protocol `<select>`, and an Add button. `PutTarget`
(`copycat/targets.go:84`) accepts the full struct, and `api.ts` types it, so this is
purely a missing control.

The fallback itself is correct and intentional -- `copycat/search.go:177-185`:

```go
func sruIndex(t Target, index string) string {
	if idx, ok := t.Indexes[index]; ok {
		return idx
	}
	switch index {
	case "isbn", "issn", "lccn":
		return "bath." + index
	}
	return index
}
```

Bath is the right default (the comment at `:165-168` explains LOC rejects
`dc.isbn`). The bug is that the preset never supplies the override.

## Why it matters

The preset row is aimed squarely at the admin who does *not* know what a PICA index
is -- it is labelled *"Suggested (open, no credentials needed)"*, and it is the only
zero-knowledge path to a working copy-cataloging source. The one server in that list
that needs configuration is the one the button configures wrong.

Two ordinary ways in:

- **Duplicate-by-default.** `suggestions` (`CopyCat.svelte:185`) filters presets by
  **name**, so `k10plus` is offered even on a stock deployment where `k10plus-sru`
  is already seeded and working. The screen invites you to add a second, broken
  K10plus alongside the good one. (`loc-sru` is correctly suppressed -- its preset
  name matches the seeded name. `k10plus` vs `k10plus-sru` is a near-miss.)
- **Rebuild after a cleanup.** `SeedDefaultTargets` seeds once ever and remembers
  (`targets.go:50-58`: *"an admin who deletes every target stays at zero across
  restarts"*). An admin who prunes targets and rebuilds from the presets cannot get
  `k10plus-sru`'s configuration back through any UI -- only through `POST
  /v1/copycat/targets` with hand-written JSON.

`sruQuery` builds `bath.isbn = "…"`, and K10plus answers either with an "Unsupported
index" diagnostic or with an empty set -- next to a `k10plus-sru` that works. Title and
author searches keep working (`dc.title` needs no override), so the target looks alive
either way.

**Correction, measured 2026-07-09** (`harness/probe_copycat_stream.mjs`, S3): this task
originally said the failure is *quiet*. Half of that is wrong. An SRU **diagnostic**
does reach the client and is rendered -- against a stub returning
`info:srw/diagnostic/1/16`, `POST /v1/copycat/search` reports
`failures: {"…": "sru: Unsupported index: bath.isbn"}`, and `CopyCat.svelte:498`
displays it. So if K10plus answers with a diagnostic, the cataloger sees a red error
naming the bad index -- confusing (they never chose an index) but not silent.

It is quiet only in the **empty-set** case, where the cataloger reads "no match for
9780…" for a book K10plus holds. Which of the two K10plus does was not tested here; this
harness does not query third-party servers. Either way the target is misconfigured
relative to `DefaultTargets`, and the fix below is unchanged.

Nothing is corrupted; this costs the cataloger the ISBN lookup that copy cataloging
is *for*.

Note on scope: this task asserts the *divergence* from libcat's own seeded
configuration, which is verified end to end. It does not independently re-verify
that sru.k10plus.de rejects `bath.isbn` -- `DefaultTargets`' comment and tasks/074,
tasks/087 are the authority for that, and this harness deliberately does not query
third-party servers.

## Expected

- **Give the preset its indexes.** `SUGGESTED_TARGETS`' `k10plus` entry should carry
  `indexes: { isbn: "pica.isb", issn: "pica.iss" }`. Better: stop maintaining a
  second copy of `DefaultTargets` in TypeScript and serve it -- e.g.
  `GET /v1/copycat/targets/suggested` -- so the two cannot drift again. The blurbs
  are the only thing the Go table lacks.
- **Forward the whole target in `addSuggested`.** `putCopycatTarget({...s})` minus
  `blurb`, so a preset with knobs keeps them.
- **Let the add form express `version`, `schema` and `indexes`.** Even a single
  "advanced" disclosure with a `key=value` index list would do. Without it,
  `PutTarget`'s full contract is reachable only by curl, and any target an admin
  creates by hand is silently Bath-only.
- **Suppress a preset that duplicates a configured URL,** not just one that
  duplicates a name -- otherwise the good `k10plus-sru` and the broken `k10plus`
  coexist and fan out to the same server twice on every search.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_copycat_targets.mjs   # T6, T6b, T7, T8
cd ~/libcat-e2e && node harness/retest.mjs                  # check t256
```

By hand, against :8481 as an admin:

```bash
TOK=…
# what the server seeds:
curl -s -H "Authorization: Bearer $TOK" localhost:8481/v1/copycat/targets \
  | jq '.targets[] | select(.name=="k10plus-sru")'
# {"name":"k10plus-sru", …, "indexes":{"isbn":"pica.isb","issn":"pica.iss"}}
```

Then open `#/copycat`, expand **Search targets**, and click
**+ K10plus German union catalogue (SRU)**:

```bash
curl -s -H "Authorization: Bearer $TOK" localhost:8481/v1/copycat/targets \
  | jq '.targets[] | select(.name=="k10plus")'
# {"name":"k10plus","url":"https://sru.k10plus.de/opac-de-627","protocol":"sru"}
#   ^ same URL as k10plus-sru, no indexes

curl -XDELETE -H "Authorization: Bearer $TOK" localhost:8481/v1/copycat/targets/k10plus
```

To see the CQL divergence without touching k10plus.de, point two targets at any
echo server and search `{"fields":[{"index":"isbn","term":"9780306406157"}]}` against
each: the one with `indexes` emits `pica.isb = "…"`, the one without emits
`bath.isbn = "…"`. That is what `probe_copycat_targets.mjs` does (T2/T3), with
`dc.title` as the control that proves the stub is not echoing noise.
