# 287 -- OPAC availability config keys are lowercased by Hugo so proxyUrl baseUrl actionUrlTemplate are silently ignored

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

`lcat-availability.js` reads four camelCase config keys:

```
cfg.baseUrl   cfg.proxyUrl   cfg.actionUrlTemplate   cfg.timeoutMs
```

`hugo/README.md` tells adopters to write exactly those in TOML (`:305`, `:326`, `:352`).
`baseof.html:45` ships the block to the browser as `{{ . | jsonify }}` of
`site.Params.availability`.

**Hugo stores param keys lowercased.** Template lookup hides this -- `.Params.proxyUrl`
resolves case-insensitively -- but `jsonify` dumps the raw map. So the page ships
`{"proxyurl": …}` and all four overrides are `undefined` at runtime.

`enabled`, `slug` and `transport` are already lowercase. They work. That is why the
feature looks configured: the gate opens, the adapter runs, and only the settings that
decide *where it fetches from* and *where the patron is sent* evaporate.

The worst case is the documented one. `transport = "proxied"` survives (lowercase);
`proxyUrl` does not. `overdriveRequest` (`lcat-availability.js:92-98`) then throws
*"overdrive.proxyUrl required for proxied transport"*, `resolve()` catches it and degrades
every id to `unknown`, and **no request is ever issued to anything**. A library that hits
Thunder's CORS wall, follows the README's fix, and deploys, gets a catalog where every
edition reads "unknown" forever, with no console error and no failed request to find in
devtools.

## Symptom

Isolated first, with a four-line Hugo site -- no libcat involved, so this is Hugo's
behaviour and not a libcat quirk:

```toml
[params]
  actionUrlTemplate = "https://borrow/{id}"
  [params.availability]
    enabled = true
    [params.availability.overdrive]
      proxyUrl = "https://p.example/av"
      slug     = "lib"
```

```
TEMPLATE-LOOKUP-camelCase: [https://borrow/{id}]        <- .Params.actionUrlTemplate  works
TEMPLATE-LOOKUP-lowercase: [https://borrow/{id}]        <- .Params.actionurltemplate  works
NESTED-LOOKUP-camelCase:   [https://p.example/av]       <- .Params.availability.overdrive.proxyUrl  works
JSONIFY-OF-THE-MAP:        {"enabled":true,"overdrive":{"proxyurl":"https://p.example/av","slug":"lib"}}
                                                          ^^^^^^^^ the only spelling the browser sees
```

Then end to end, building `hugo/exampleSite` with `[params.availability]` as the README
prescribes and driving it in a real browser with the provider HTTP stubbed:

```
A2  direct transport, stubbed Thunder
      data-status="available"  text="Available now"   (1 Thunder request)   <- the DOM wiring is fine

A3  keys as they arrive in the browser
      ["baseurl","actionurltemplate","slug"]                                <- camelCase gone

A5  actionUrlTemplate = "https://borrow.example/go/{id}"
      rendered borrow link = "https://queerliblib.overdrive.com/media/24760f5d-…"
                                                                            <- template ignored

A4  transport = "proxied", proxyUrl = "http://127.0.0.1:8477/proxy"   (README.md:325-327)
      proxy received 0 requests
      Thunder received 0 requests
      placeholder reads "unknown"
```

`A4` is the one that matters. `unknown` is also what a *failed* fetch renders (`A6`
confirms that path works and is correct). The difference is invisible on the page and
decisive underneath: **the proxy was up, reachable and stubbed, and was never asked.**

## Root cause

`hugo/layouts/baseof.html:45`:

```go-html-template
<script id="lcat-availability-config" type="application/json">{{ . | jsonify }}</script>
```

`.` is `site.Params.availability`, a Hugo `maps.Params`, whose keys were lowercased when
the config was loaded. `jsonify` serializes the map as stored.

`hugo/assets/lcat-availability.js` then reads:

```js
function overdriveRequest(ids, cfg) {
  if (cfg.transport === "proxied") {              // "transport" survives -- lowercase already
    if (!cfg.proxyUrl) {                          // undefined: the page shipped "proxyurl"
      throw new Error("lcat-availability: overdrive.proxyUrl required for proxied transport");
    }
    ...
  }
  var base = cfg.baseUrl || THUNDER_BASE;         // override silently dropped
}

function overdriveActionUrl(reserveID, cfg) {
  if (cfg && cfg.actionUrlTemplate) { ... }       // never taken
  if (cfg && cfg.slug) return "https://" + cfg.slug + ".overdrive.com/media/" + reserveID;
}
```

and `daiaRequest` (`:225-233`) requires `cfg.baseUrl` for `direct` and `cfg.proxyUrl` for
`proxied`, so **both** DAIA transports throw on any real site (see also tasks/288, which
stops DAIA before it ever gets this far).

`readConfig` (`:465-475`) does no key normalization -- it is `JSON.parse` plus an `enabled`
check. `overdriveActionUrl` is `:60-67`.

## Why the tests are green

`hugo/availability_test.cjs` is 23 tests deep, and its header is accurate:

> *"Exercises the OverDrive/Thunder mapping, batching, cache, in-flight de-dup, and error
> degradation with an injected fetch -- no network, no DOM."*

Every one of them hands the adapter a **hand-written JavaScript object**:

```js
test("overdriveRequest: direct hits Thunder, proxied hits the proxy", () => {
  A.overdriveRequest(["a"], { transport: "proxied", proxyUrl: "https://p.example" })
```

That object has the spelling the code reads, because a human wrote it in the same file. The
seam that breaks is **TOML → Hugo → `jsonify` → `readConfig` → adapter**, and nothing in
libcat crosses it: `init()` and `collect()` are called by no test, `readConfig` is tested
against a `fakeDoc` whose JSON a human also wrote in camelCase, and no
`hugo/e2e/*.spec.mjs` mentions availability.

This is the second time a libcat defect has lived exactly where the fixture stops (tasks/285:
`exampleSite` sets `covers = true` and ships zero covers). The pattern is worth naming: **a
unit test that constructs its own input cannot discover that the real producer spells things
differently.**

## Why it matters

**It silently disables the feature in its most likely production configuration.** Thunder is
a public API called directly from the browser; CORS blocking a deploy origin is the normal
case, and the README's answer is `transport = "proxied"`. Following that instruction
produces a catalog where nothing is ever available and nothing anywhere reports an error --
`resolve()` degrades to `unknown`, `renderInto` writes it, and the patron sees a blank or
"unknown" chip on every edition of every book.

**`actionUrlTemplate` is the borrow button.** Ignored, every patron is sent to
`{slug}.overdrive.com/media/{id}` instead of the library's configured borrow flow. That is
a live, wrong link on every edition, not a missing one.

**It is undetectable from the outside.** No 4xx, no console error (`resolve` swallows), no
network request to notice missing. The only visible symptom is a status that never becomes
"available" -- which is exactly what an honest degradation looks like.

## Expected

- **Read the config case-insensitively, once, at the boundary.** `readConfig` is the single
  place every adapter's config flows through. Normalize there and no adapter changes:

  ```js
  function normalizeKeys(o) { /* recursively lowercase keys, or build a case-folded lookup */ }
  ```

  Then have the adapters read the normalized spelling, or expose a `pick(cfg, "proxyUrl")`
  helper. Do not fix it by renaming the four keys to lowercase in the JS alone -- an adopter
  who writes `proxyurl` in TOML must keep working, and so must one who writes `proxyUrl`,
  because Hugo accepts both and the README documents the second.

- **Or serialize the keys the adapter reads.** `baseof.html` can emit an explicit dict
  rather than dumping `maps.Params`:

  ```go-html-template
  {{ $od := .overdrive }}
  {{ $cfg := dict "enabled" .enabled "overdrive" (dict "slug" $od.slug "transport" $od.transport "proxyUrl" $od.proxyUrl "baseUrl" $od.baseUrl "actionUrlTemplate" $od.actionUrlTemplate "timeoutMs" $od.timeoutMs) }}
  ```

  because `$od.proxyUrl` **does** resolve -- template lookup is case-insensitive. This is the
  smaller diff and keeps the JS honest about its own contract. It needs the same treatment
  for `daia`.

- **Fail loudly when a configured transport cannot build a request.** `resolve()` catching
  every error and degrading to `unknown` is right for a *network* failure and wrong for a
  *configuration* one. A thrown `"proxyUrl required"` should reach `console.error` at least
  once. Today the message exists, is correct, and is seen by nobody.

- **Test the seam.** One `hugo/e2e` spec that builds a site from TOML, serves it, stubs the
  provider, and asserts the placeholder fills. That single test catches this, tasks/288, and
  the next one.

## Repro

```bash
cd ~/libcat-e2e && node harness/probe_opac_availability.mjs   # A3, A4, A5 (A7 = tasks/288)
cd ~/libcat-e2e && node harness/retest.mjs                    # check t287
```

The probe builds `hugo/exampleSite` twice in a scratch directory -- once with the README's
`direct` transport, once with its `proxied` transport -- serves each over http, routes the
provider requests with Playwright, and reads the `<span data-availability>` placeholders. It
never writes inside `~/libcat` and touches no running site.

Its controls carry the argument. `A0` shows the config script and module loaded. `A1` shows
the editions carry `data-overdrive-reserve` and hold a placeholder. **`A2` shows the DOM
wiring works** -- a stubbed Thunder answer renders `data-status="available"`, `"Available
now"` -- so `A3`/`A4`/`A5` are statements about config, not about `collect()`. **`A6` shows a
genuinely failed fetch degrades to `unknown`**, which is the control that makes `A4`'s
`unknown` mean something: same rendered text, zero requests issued.

By hand, with four lines of Hugo and no libcat:

```toml
[params.availability]
  enabled = true
  [params.availability.overdrive]
    proxyUrl = "https://p.example/av"
```
```go-html-template
{{ site.Params.availability.overdrive.proxyUrl }}   {{/* https://p.example/av */}}
{{ site.Params.availability | jsonify }}            {{/* {"enabled":true,"overdrive":{"proxyurl":"…"}} */}}
```
