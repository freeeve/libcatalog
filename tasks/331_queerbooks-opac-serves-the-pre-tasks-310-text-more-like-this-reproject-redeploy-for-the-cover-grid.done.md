# 331 -- queerbooks OPAC serves the pre-tasks/310 text more-like-this; reproject+redeploy for the cover grid

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

## Symptom

The live queerbooks OPAC (:8502) renders "More like this" as a text list, not the
cover grid. Measured markup on `/works/wdnfk5tiirq5ro/`:

```
class="lcat-similar"       x1
class="lcat-similar-list"  x1   <- old list container
class="lcat-similar-item"  x8
class="lcat-similar-note"  x1   <- old visible explanation line
class="lcat-similar-why"   x8   <- old per-tile "why" text
class="lcat-similar-term"  x13
```

None of the cover-grid classes are present: no `lcat-similar-grid`,
`lcat-similar-art`, `lcat-similar-cover`, `lcat-similar-badge`, `lcat-similar-by`.

The playground OPAC (:8482), built from current source, renders the grid on the
same feature -- `/works/werhruvav8t7qu/`:

```
class="lcat-similar-grid"  x1
class="lcat-similar-art"   x8
class="lcat-similar-cover" x8  (all --none: playground test data has no cover art)
class="lcat-similar-badge" x8
class="lcat-similar-by"    x8
class="lcat-similar-title" x8
class="lcat-similar-link"  x8
```

## Root cause

Not a source defect -- a stale deploy. The cover grid landed in commit `627ea2f`
"feat(hugo): 'more like this' is a cover grid with carrier badge and contributors
(tasks/310)". The old text markup (`lcat-similar-list` / `lcat-similar-why` /
`lcat-similar-note`) is fully removed from current `hugo/layouts/page.html` --
`grep -rn 'lcat-similar-list\|lcat-similar-why\|lcat-similar-note' hugo/` returns
nothing. So the deployed queerbooks OPAC was projected+built with a Hugo module
(and lcat) predating tasks/310. The grid is the unconditional default in current
source (`hugo/layouts/page.html:142-222`, no config gate); nothing needs to be
turned on.

The grid's covers/badges/contributors are joined per neighbour by the content
adapter, not carried in the sidecar: `hugo/content/works/_content.gotmpl:275,287-289`
does `$n := index $works .id` and reads `$n.extra.cover`, `$n.contributors`, and a
format `badge` from `$n.formats`. `similar.json` itself holds only `{id,title,shared}`
(`project/similar.go:25` SimilarNeighbor; written by `cmd/lcat/project.go:186`).

Queerbooks works already carry the cover art the grid needs: the main work page
renders `<img class="lcat-cover lcat-cover--detail" src="https://img1.od-cdn.com/...IMG400.JPG">`
and an `og:image` with the same URL. Those same works are the neighbours in other
works' rails, so after a reproject+redeploy their `extra.cover` populates the tiles
-- the POC look, with real covers.

## Why it matters

The cover grid is the intended default libcat OPAC view (tasks/310); queerbooks is
the flagship live catalog and is the one place still showing the superseded text
list. It is purely a deploy-version lag, so the fix is operational and low-risk.

## Expected

Reproject queerbooks (`lcat project ...`, current lcat -- keep `--similar` > 0) and
rebuild/redeploy its OPAC with a Hugo module at or past `627ea2f` (tasks/310). The
"More like this" rail then renders the cover grid with od-cdn covers, format badges,
and contributor credits, matching the playground and the POC.

## Repro

```
# old text markup live on queerbooks:
curl -s http://localhost:8502/works/wdnfk5tiirq5ro/ | grep -oE 'class="lcat-similar[^"]*"' | sort | uniq -c
# cover grid on the current-source playground build:
curl -s http://localhost:8482/works/werhruvav8t7qu/ | grep -oE 'class="lcat-similar[^"]*"' | sort | uniq -c
# old markup gone from source, grid ungated:
grep -rn 'lcat-similar-list\|lcat-similar-why\|lcat-similar-note' ~/libcat/hugo/   # -> empty
git -C ~/libcat log --oneline -1 627ea2f
```

## Note

No matching retest check added to harness/retest.mjs: the harness asserts against
current source (playground :8482), which already renders the grid, so there is
nothing source-side to regress. The remaining action is the queerbooks redeploy,
which is outside the harness's reach.

## Outcome (2026-07-10) -- CLOSED, misfiled; no libcat change

Not a libcat issue and already tracked in the right repo. queerbooks is a separate
repo (`~/queerbooks-demo/`) that adopts libcat in lockstep. Measured pins/versions:

- `~/queerbooks-demo/site/go.mod` pins `github.com/freeeve/libcat/hugo v0.126.0`.
- v0.126.0 = commit `c3fdeb7` (2026-07-10 07:28); the cover-grid commit `627ea2f`
  (tasks/310) landed 08:17, 49 min later. `git merge-base --is-ancestor 627ea2f
  v0.126.0` -> false: v0.126.0 predates the grid.
- The grid first appears in module tag **v0.128.0** (latest is v0.141.2).

The adoption is already queued in queerbooks-demo, so nothing new needs filing:

- queerbooks-demo `tasks/074` -- "libcat v0.128.0: the more-like-this rail is now a
  cover grid ... (tasks/310)" (pending heads-up).
- queerbooks-demo `tasks/077` -- "adopt libcat v0.141.2 lockstep ... 310 cover-grid
  rail ..." (pending). Executing 077 (bump the hugo module pin + matching lcat,
  reproject, redeploy) is the whole fix; queerbooks works already carry od-cdn
  covers, so the tiles populate.

libcat itself is correct (grid shipped v0.128.0, default, ungated). Closing.
