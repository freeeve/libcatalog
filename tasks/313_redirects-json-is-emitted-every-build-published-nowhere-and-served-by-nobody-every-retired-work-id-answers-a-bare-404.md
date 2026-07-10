# 313 -- redirects.json is emitted every build, published nowhere, and served by nobody -- every retired work id answers a bare 404

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

## Symptom

The projector writes `redirects.json` on every build. The published site never
receives it, and no retired id ever redirects.

Measured read-only against the published playground OPAC on `:8482`
(`harness/probe_opac_redirects.mjs`, 4/7 -- the 3 failures are this bug):

```
redirects.json (schema v11)         2710 retired ids
                                       4 merged, with a surviving id
                                    2706 tombstoned
published catalog                     35 works

GET /redirects.json                  404
GET /catalog.json                    404      <- correct: a build input
GET /facets.json                     404      <- correct: a build input
GET /favicon.svg                     200      <- static/ IS published

6 of 6 sampled retired ids  ->  404, Location: none, meta refresh: none
```

Not one of the 2710 answers a `301`, a `410`, or a meta refresh. The reader who
follows a two-year-old link to a merged work gets the same generic 404 as
someone who mistyped an id.

## Root cause

Two independent halves, both mechanical.

**The map is written to a directory the host never sees.** `docs/build-pipeline.md:37`:

```toml
[project] out = "site/assets"            # catalog.json + facets.json + redirects.json + similar.json
[export]  out = "site/static/downloads"
```

Hugo publishes `static/` verbatim and publishes `assets/` only through a template
that pipes a resource. `catalog.json` and `facets.json` belong in `assets/` --
they are build inputs, consumed by the content adapter and discarded.
**`redirects.json` is the one artifact whose only consumer is the host at
runtime, and it is the one written to the directory the host never sees.**
That is why `/catalog.json` 404ing is correct and `/redirects.json` 404ing is not.

**Nothing reads it even where it lands.** `grep -rn "redirect" hugo/` returns
nothing: no template, no `aliases` front-matter, no `_redirects`, no
`netlify.toml`, no `vercel.json`. `cmd/lcat/serve.go` has no redirect handling
of any kind.

The contract was written down and then nobody built the second half.
`project/project.go:492-495`:

> `RedirectMap` is the redirect artifact emitted alongside `catalog.json`: every
> retired Work id and the surviving id it now resolves to ... The static host
> turns each into a 301 (per the `tasks/001` decision: the projector emits the
> map, the host serves it).

The projector holds up its end. There is no host.

## Why it matters

`docs/ARCHITECTURE.md:103` states the promise this artifact exists to keep:

> Because a Work's public URL derives from its opaque id, a merge or split must
> leave a redirect/tombstone so shared links and SEO survive (see `tasks/001`).

It does not hold. 2710 URLs that were once public and citable now answer 404.
For a library catalog this is the failure mode that matters most: a permalink in
a syllabus, a citation, a LibGuide, an inbound link from Wikipedia. A merge is
an *editorial* act -- the work still exists, under a different id -- and the
catalog currently punishes the reader for the cataloguer's housekeeping.

Search engines read a bare 404 as "this was never here." A `410` says "this is
gone on purpose"; a `301` moves the accumulated authority to the survivor. The
map to do both is sitting on disk, one directory away, regenerated on every build.

This is a fifth instance of the family behind tasks/115, 261, 300 and 305: **the
durable record of an intention is written, and nothing carries the intention
out.** Here the record is `redirects.json` and the intention is "this link still
works."

## Expected

1. The map reaches the published site. Either `[project]` writes `redirects.json`
   to `static/` (leaving `catalog.json` / `facets.json` / `similar.json` in
   `assets/`, where they belong), or a Hugo template pipes it out.
2. Retired ids answer. Hugo's alias machinery is already running in this very
   build -- `GET /page/1/` serves
   `<meta http-equiv="refresh" content="0; url=http://localhost:8482/">` -- so
   emitting an alias page per merged id needs no new mechanism, only a content
   adapter that reads the map it is already handed. That covers the static-host
   case with no host configuration at all.
3. The two entry kinds get different answers:
   - **merged** (`to` is set, 4 here) -> `301` to the survivor, or the alias page.
   - **tombstoned** (`to` is empty, 2706 here) -> `410 Gone` with a short
     explanatory page, not `404`.
   Note that on this build all 4 merge targets are themselves tombstoned, so a
   correct host answers `301` and then `410`. That is the right answer, and a
   test must not demand a `200` at the end of the chain.
4. `cmd/lcat/serve.go` should serve the same two answers, so `lcat serve` and a
   static host agree.

## Repro

```
cd ~/libcat-e2e && node harness/probe_opac_redirects.mjs     # 4/7; R0-R3 pass, D1-D3 fail
node harness/retest.mjs                                       # t313
```

Or directly, against any built OPAC:

```
jq '.redirects | length' ~/libcat-playground/opac/assets/redirects.json   # 2710
curl -s -o /dev/null -w '%{http_code}\n' localhost:8482/redirects.json    # 404
curl -si localhost:8482/works/<any id from the map>/ | head -1            # 404, no Location
```
