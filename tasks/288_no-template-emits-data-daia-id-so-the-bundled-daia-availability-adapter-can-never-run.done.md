# 288 -- no template emits data-daia-id so the bundled DAIA availability adapter can never run

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

`lcat-availability.js` registers two adapters (`:276-288`):

```js
registerAdapter({ providerKey: "overdrive", domAttr: "data-overdrive-reserve", … });
registerAdapter({ providerKey: "daia",      domAttr: "data-daia-id",           … });
```

`collect()` (`:480`) finds each adapter's editions with
`doc.querySelectorAll("[" + adapter.domAttr + "]")` (`:484`), and `init()` (`:499`) only
creates a job for a provider that `collect()` found elements for.

```
$ grep -rn 'daia' hugo/layouts/ hugo/content/
$        # nothing
```

**No libcat template emits `data-daia-id`.** The DAIA adapter -- ~120 lines,
`daiaItemStatus` / `daiaItemLocation` / `normalizeDaia` / `daiaRequest` / `fetchDaiaBatch`,
six unit tests, a README section, and a row in `docs/availability-providers.md` -- cannot
fire on any page libcat builds.

Meanwhile `hugo/README.md:355` says, as a statement of fact about this module:

> *"Editions carry `data-daia-id` (the DAIA document id)."*

They do not.

## Symptom

Built `hugo/exampleSite` with `[params.availability]` enabling both adapters, added a DAIA
identifier to one Work's instance, served it, and stubbed the DAIA endpoint with a Playwright
route:

```
catalog:  wexampletwo.instances[0].providerIds += { "source": "daia", "value": "ppn:e2e-daia-1" }
config:   [params.availability.daia]  baseUrl = "http://127.0.0.1:8477/daia"

A7  the Work page renders 1 edition(s), 0 carrying data-daia-id,
    and the DAIA adapter issued 0 request(s)
```

Zero requests. `collect()` found nothing to ask about, so `init()` created no DAIA job. The
stub endpoint was up the whole time and was never contacted.

The same run shows the machinery is otherwise healthy, which is what makes the zero
attributable:

```
A2  overdrive, direct transport, stubbed Thunder
      data-status="available"  text="Available now"   (1 Thunder request)
```

Same page, same `collect()`, same `renderInto()`. The only difference is the attribute.

## Root cause

`hugo/layouts/page.html:86` is the only place an edition's identifiers reach the DOM:

```go-html-template
<li class="lcat-edition" data-instance="{{ .id }}"
    {{- with .format }} data-format="{{ . }}"{{ end }}
    {{- range .providerIds }}{{ if eq .source "overdrive-reserve" }} data-overdrive-reserve="{{ .value }}"{{ end }}{{ end }}>
```

One hardcoded scheme, one hardcoded attribute. A `providerIds` entry with any other
`source` is dropped on the floor.

The projector is not the blocker. `instances()` (`project.go:1087`) passes **every**
non-ISBN identifier through into `providerIds` with its `bf:source` scheme:

```go
pids = append(pids, ProviderID{Source: p.identifierSource(id), Value: v})
```

`availabilitySources` (`:1067`, `{"overdrive-reserve": true}`) gates only the `held` flag
(`:1116`, tasks/078), not the projection. And `ProviderID`'s own doc comment (`:261-263`)
states the intent plainly:

> *"so a client-side availability adapter selects its key by scheme (e.g. OverDrive's
> `overdrive-reserve` Reserve ID vs the `overdrive` title id) rather than guessing from a flat
> list"*

The data model is right, the reader is right, and the template in between knows about
exactly one provider.

**There is also no defined `bf:source` scheme for a DAIA document id.** Nothing in libcat
names one, so an adopter with a Koha or GBV endpoint has no way to say "this identifier is
the DAIA id" even if the template were fixed. That is the second half of the fix, and it is
why this is not a one-line change.

## Why it matters

**It is a shipped, documented, tested feature that cannot be switched on.** `README.md:340-364`
sells DAIA as the proof of the digital/physical superset -- *"it populates `locations[]`
(per-branch shelf location, call number, status, and due date) that the digital adapters
leave empty"* -- and `docs/availability-providers.md:51` calls it the adapter that "proves the
superset". A library with physical holdings configures it exactly as documented, deploys, and
gets nothing: no request, no error, no rendered status.

**Physical holdings are most libraries.** The one bundled adapter that works, OverDrive, is
digital-only. The catalog's answer to "is this book on the shelf?" is code that runs in no
browser.

**The unit tests certify the unreachable half.** Six of the 23 tests in
`availability_test.cjs` exercise DAIA -- `daiaItemStatus`, `normalizeDaia`, `daiaRequest`,
`resolve(daia)` twice, and `statusText: physical holding shows shelf location and due date`.
All pass. All call the pure core directly. None can observe that `collect()` will never hand
it an id, because `init()` and `collect()` are called by no test in the repo, and no
`hugo/e2e/*.spec.mjs` mentions availability.

## Expected

- **Define the `bf:source` scheme for a DAIA document id** (e.g. `daia`, or `daia-ppn`), and
  say so where `ProviderID` documents scheme selection (`project.go:261-263`) and in
  `hugo/README.md`.

- **Emit the attribute from the scheme, generically.** `page.html:86` should map schemes to
  adapter attributes rather than hardcoding one, so registering an adapter and projecting its
  identifier is enough:

  ```go-html-template
  {{- range .providerIds }}
    {{- if eq .source "overdrive-reserve" }} data-overdrive-reserve="{{ .value }}"
    {{- else if eq .source "daia" }} data-daia-id="{{ .value }}"{{ end }}
  {{- end }}
  ```

  A table in `site.Params` or a module `data/` file would let a third adapter arrive without
  touching the layout at all -- which is what `registerAdapter` already promises on the JS
  side.

- **Fix `README.md:355`.** *"Editions carry `data-daia-id`"* is false today. Either make it
  true or mark the adapter as requiring a theme override -- because an adopter *can* reach it
  by shadowing `page.html`, and nothing says so.

- **Give `exampleSite` a DAIA identifier**, the way tasks/285 concluded the cover slot needed
  a cover. Its catalog has three works, all OverDrive, all digital. The physical path has no
  fixture anywhere in the module.

- Note that even once the attribute is emitted, DAIA still cannot fetch: `daiaRequest`
  requires `cfg.baseUrl` (direct) or `cfg.proxyUrl` (proxied), and Hugo lowercases both out
  of existence (**tasks/287**). The two must land together for DAIA to work at all.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_opac_availability.mjs   # A7 (A3, A4, A5 = tasks/287)
cd ~/libcat-e2e && node harness/retest.mjs                    # check t288
```

The probe builds `hugo/exampleSite` in a scratch directory with a DAIA identifier injected
and both adapters configured, serves it over http, and routes the DAIA endpoint so a request
would be recorded if one were made. It never writes inside `~/libcat` and touches no running
site.

`A2` is the control that gives `A7` its meaning: on the same build, through the same
`collect()` and `renderInto()`, the OverDrive edition renders `data-status="available"` from
a stubbed Thunder answer. The DOM wiring works. Only DAIA is unreachable.

By hand:

```bash
grep -rn 'daia' ~/libcat/hugo/layouts/    # no output
sed -n '355p' ~/libcat/hugo/README.md     # "Editions carry `data-daia-id` (the DAIA document id)."
sed -n '283,288p' ~/libcat/hugo/assets/lcat-availability.js   # domAttr: "data-daia-id"
```

## Outcome

Fixed in **v0.117.0** (`83d2f2d`), on top of tasks/287 (v0.116.3) which had to land
first: even once the attribute was emitted, `daiaRequest` needed a `baseUrl` that
Hugo had lowercased out of existence. Both were needed for DAIA to work at all,
exactly as the report's last bullet said.

### The fix

Took the second remedy -- the table -- rather than the `else if` chain, because the
`else if` reproduces this bug for adapter number three. `page.html` now names no
provider:

```toml
# data/lcat/availabilityAttrs.toml    (bf:source scheme -> adapter domAttr)
"overdrive-reserve" = "data-overdrive-reserve"
"daia"              = "data-daia-id"
```

Site data merges over module data **per key**, so a deployment can wire its own
adapter by shipping one row, without shadowing `page.html`. Pinned by a test.

Scheme named `daia`, matching the JS `providerKey`, over `daia-ppn`: the PPN is one
ILS's identifier flavour, not the scheme. Documented on `ProviderID` (`project.go`)
and in `hugo/README.md`, which gained a "Wiring an edition to an adapter" section.

`exampleSite` gains a print edition on wexampletwo with a `daia` document id and an
item (call number, shelving location, barcode) -- the module's first physical-holdings
fixture, per the report's fourth bullet. The only new pages are the `print` format
taxonomy, in both languages (120 -> 124); a11y and link gates clean.

### `html/template` will not compute an attribute name

The interesting part, and worth recording because the naive fix is a trap. Go's
`html/template` does not error on `{{ $attr }}="{{ $v }}"` in tag context -- it emits
a sentinel:

```html
<li class="lcat-edition" ZgotmplZ="a&#34;b&lt;c">
```

Silent, unusable, and the same failure class as the bug being fixed. So the attribute
is built with `safeHTMLAttr` -- which means the *value* is no longer escaped by the
context autoescaper and must be escaped explicitly (`htmlEscape`). Without that, a
catalogued value of `a"b<c` closes the attribute. Both halves verified with a
standalone `html/template` probe rather than assumed.

`safeHTMLAttr` also lets a hostile table row inject an attribute *name*, so emission
is gated on `findRE "^data-[a-z0-9-]+$"`. A site can override the table via its own
`data/` file, so that is a reachable input, not a typo guard.

### Testing the seam

Folded into `hugo/availability_seam_test.cjs` rather than a new file, because the
report asked for *one* test that catches this, tasks/287, and the next one. It builds
`exampleSite` for real and asserts on the render.

The load-bearing assertion is not "`data-daia-id` is present" but:

```js
const missing = Object.keys(A.adapters).filter((k) => !html.includes(A.adapters[k].domAttr + "="));
```

Every **registered adapter's** `domAttr` must appear on a real rendered edition. A
future adapter that nobody wires into the table fails the build instead of shipping
dead -- which is the actual defect class here. It asks the adapter registry, not a
hardcoded list.

Mutation-proved every guard:

| mutation | caught by |
|---|---|
| M1 `page.html` back to the hardcoded scheme | adapter-reachability, DAIA id, hostile-row |
| M2 `daia` row dropped from the table | adapter-reachability, DAIA id |
| M3 `findRE` name guard removed | hostile-row (emits `onload=alert(1)="…"`) |
| M4 `overdrive` title id mapped into the table | "a scheme with no row emits no attribute" |
| M5 `daia` providerId removed from the fixture | adapter-reachability, DAIA id |
| M6 naive `{{ $attr }}="{{ .value }}"`, no `safeHTMLAttr` | **all five**, incl. ZgotmplZ |

M6 is the one worth keeping. The `ZgotmplZ` assertion survived M1-M5 untouched -- an
unfalsified test -- until M6 showed it is exactly what stands between a table-driven
template and a silent re-run of this bug.

### End to end

`exampleSite` with both adapters configured, DAIA endpoint stubbed, driven in jsdom.
`wexampletwo` now has one digital and one physical edition:

```
before (hardcoded page.html, fixture already carrying the daia id)   <- the report's A7
    DAIA requests: 0    <li … data-instance="iextwoprint" data-format="print">
    iextwoprint: status=null  text=""

after
    DAIA requests: 1    <li … data-instance="iextwoprint" … data-daia-id="ppn:example-daia-1">
    iextwoprint: status=available  text="Available now · Main Library, Fiction · PQ8098.1.L54 C313"
```

The second line is the first time the DAIA adapter has run in this repo, and it renders
the shelf location and call number that `docs/availability-providers.md` calls the proof
of the superset.

### Deliberately not done

**`availabilitySources` unchanged.** Adding `daia` would flip `Instance.Held` for a
record with a DAIA id and no projected items, changing the `holdings` facet counts of
every adopting catalog. The report does not ask for it, and `Held`'s doc comment scopes
it to the *digital*-holding signal -- physical holdings set it from `items`. Worth a
separate task if a library wants a DAIA id alone to mean "held".

`docs/availability-providers.md:45` claimed "adding a physical ILS does not change the
templates". That was false when written and is true now; the line is annotated rather
than deleted, since it records the intent the code has finally caught up to.
