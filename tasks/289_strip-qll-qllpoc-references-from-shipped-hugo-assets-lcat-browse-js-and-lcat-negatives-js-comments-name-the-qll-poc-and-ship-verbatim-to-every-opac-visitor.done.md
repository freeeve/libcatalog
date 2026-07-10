# 289 -- strip QLL/qllpoc references from shipped hugo assets -- lcat-browse.js and lcat-negatives.js comments name the QLL POC and ship verbatim to every OPAC visitor

Filed from queerbooks-demo on 2026-07-10 (cross-repo ask).

## What

Two hugo-module assets carry the QLL name in source comments, and hugo
publishes them to the site unminified, so the strings are served to every
visitor of any adopter's OPAC:

    hugo/assets/lcat-browse.js:39    * Live facet counts (tasks/177, the QLL POC's model): while a query or
    hugo/assets/lcat-browse.js:290   // ---- Subject vocabulary trees (tasks/174, ported from the QLL POC) ----
    hugo/assets/lcat-negatives.js:3  * facet term. URL state is x<taxonomy>=<term>, repeatable, matching the qllpoc

Confirmed present in a v0.114.0 render:

    site/public/lcat-browse.<hash>.js     -> "QLL"
    site/public/lcat-negatives.<hash>.js  -> "qllpoc"

`hugo/README.md:166` also mentions the qllpoc convention, which is fine --
the README is not published by adopters.

## Why it matters here

queerbooks-demo's tasks/054 removed every QLL mention from its public OPAC
face (branding, provenance allowlist, holdings tags) because the deployment
must not name that institution publicly. Adopter-side that job is done, but
these two module assets reintroduce the name in the published bundle and an
adopter cannot fix it without forking the module.

The audit we run after each adoption greps the render for /qll/i; these are
now the only real hits (everything else is a WorkID/InstanceID coincidence
like `w9qtvsqll3qhmo`), so they also cost us a clean signal.

## Ask

Reword the three comments to name the mechanism rather than the institution
-- e.g. "the live-facet-count model", "ported from the earlier POC", "URL
state is x<taxonomy>=<term>, repeatable, so a filtered view is shareable".
No behavior change; the comments carry no information that depends on the
name.

Nice-to-have: keep publishable assets free of internal project names
generally, since anything under `hugo/assets/` is served as-is.

## Outcome

Fixed in **v0.116.2** (`292cb81`). All three comments reworded; the nice-to-have
is now a guard that runs in `npm run test:js`.

The three strings carried no information that depended on the name, exactly as the
report says:

```
lcat-browse.js:39     "(tasks/177, the QLL POC's model)"        -> "(tasks/177)"
lcat-browse.js:290    "ported from the QLL POC"                 -> "ported from an earlier POC"
lcat-negatives.js:3   "matching the qllpoc convention, so..."   -> "repeatable, so..."
```

### The guard, and what it taught me

`hugo/publishable_names_test.cjs` fails on `qll`, `qllpoc` or `queerbooks` in
anything Hugo publishes verbatim. It checks **sources, not a render**, for the
reason the report gives from the other side: a built page legitimately contains
those letters inside opaque ids (`w9qtvsqll3qhmo`), so a grep over the render
cannot be strict. A grep over the sources can.

It scans `assets/` byte-for-byte, and `layouts/` **only for `<!-- -->` HTML
comments**, because a Go-template comment is compiled away and never served.

That distinction was not decoration. My first version scanned `layouts/` whole and
immediately reported a fourth hit:

```
layouts/_partials/facets.html:9   (queerbooks: 8 of 9.1GB)
```

which is inside a `{{- /* ... */ -}}` comment -- a real measurement, correctly
cited, and invisible to every visitor. Scoping the check to what Hugo actually
emits cleared it without having to weaken the pattern list.

Proven by reverting the three comments: the guard reports exactly those three and
exits 1. A rebuilt `exampleSite` now contains **no case-insensitive match for
"qll" anywhere in the render**, which is the signal the downstream audit wanted
back.

`hugo/README.md`'s mention is untouched: adopters do not publish it.

### Adoption

Rebuild the site. Comments only -- no behavior change, no config change, no
template output change. Patch release.

The downstream `/qll/i` audit over a fresh render should now come back empty apart
from WorkID coincidences, which is what it was asking for.
