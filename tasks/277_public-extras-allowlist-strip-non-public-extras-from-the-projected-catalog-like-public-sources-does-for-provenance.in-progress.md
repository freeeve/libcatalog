# 277 -- public-extras allowlist: strip non-public extras from the projected catalog

Filed from queerbooks-demo on 2026-07-09 (cross-repo ask).

`[project] public-sources` already gives us exactly the right shape for
provenance: project.SanitizeSources drops every extra.sources name not on
the allowlist, so private community sources never reach catalog.json, the
facets, or the dumps. There is no equivalent for the other extras.

Our need (queerbooks tasks/054): the corpus carries holdings extras --
`inQll`, `qllEbook`, `qllAudiobook` (62,602 / 12,539 / 7,100 works) --
that are institution-private: they say which titles one library already
holds. They must stay in the grains (the cataloging backend is exactly
where librarians need them, and they drive the acquisition map) but they
must not ride the projected catalog.json, the browse artifacts derived
from it, or the downloads.

Today the only way to keep them out of the public face is not to emit
them in ingest at all, which loses them for the backend too -- the two
faces project from the same grains, which is the design.

Ask: `[project] public-extras` (and the matching `--public-extras` flag
plus an `[export]` override, mirroring public-sources exactly):

    [project]
    public-sources = ["loc", "overdrive queer scan"]
    public-extras  = ["cover", "rating", "ratingsCount", "series",
                      "seriesOrder", "audience", "authorLiving"]
    # everything else in extra{} is dropped from the public catalog

Semantics we would want, again by analogy:
- Empty/absent = keep everything (today's behavior; no silent break).
- The allowlist governs catalog.json, whatever the browse/index build
  derives from it, and the three dumps -- the same surfaces
  public-sources governs. `sources` itself stays under public-sources.
- Grains untouched: this is a projection-time filter, not an ingest one.
- A count of what was stripped in the build log, like SanitizeSources
  reports (`stripped N sources`), so a misconfigured allowlist is loud.

Sharp edge worth handling: extras also feed [params.extraFacets] and any
site template reading .Params.<extra>. A stripped extra simply becomes
absent, so templates guarding with `with` degrade cleanly -- but a facet
configured on a stripped extra should probably warn at build time rather
than render an empty rail.

Until this lands we are deleting the chip/badge templates on our side, so
the data stops being *rendered* -- but inQll/qllEbook/qllAudiobook still
sit in the published catalog.json for anyone who fetches it. That is the
gap this ask closes.
