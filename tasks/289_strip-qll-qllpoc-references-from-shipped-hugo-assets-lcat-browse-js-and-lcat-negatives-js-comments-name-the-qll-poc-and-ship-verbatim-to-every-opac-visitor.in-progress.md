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
