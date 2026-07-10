# 283 -- document the release versioning policy: patch for fixes, minor for additive or breaking

Opened 2026-07-10, from Eve, who noticed that six releases in one day were all
minors and that "a lot of these are minor fixes that could be patch versions".

She is right. `scripts/release.sh` accepts any `vX.Y.Z` and this repo has cut
patch releases before (`v0.4.1`, `v0.7.2`, `v0.100.1`, `v0.103.1`). Nothing was
enforcing the reflex; there was simply no written rule, so every release took the
minor slot.

## Outcome

Wrote `docs/versioning.md` and a short binding summary in `CLAUDE.md`, so the
policy is both explained and in front of the next session before it releases.

The rule: **patch** when the release only makes wrong behavior right and the
adoption note is "rebuild and restart"; **minor** when the consumer has something
to do, whether additive (a new field to render) or breaking (a required header).
Highest wins in a mixed release.

The corollary worth keeping is that a bug fix a client could have been *relying
on* is not a patch. tasks/253 is the live example: making facets list selected
values with `"count": 0` will re-hide the filter for any client that strips
zero-count values. It reads like a pure fix and it has an adoption note, so it
earns the minor. The number is cheap; a surprised consumer is not.

Audit of the six releases cut on 2026-07-09/10, recorded because the misses are
the point:

| release | task | slot cut | slot earned |
|---|---|---|---|
| v0.109.0 | 258 | minor | minor (additive `warnings` map) |
| v0.110.0 | 272 | minor | **patch** (no correct client read the leaked paths) |
| v0.111.0 | 274 | minor | minor (the copycat error contract changed) |
| v0.112.0 | 273 | minor | minor (breaking: `If-Match` now required) |
| v0.113.0 | 280 | minor | minor (breaking: the default result set changed) |
| v0.114.0 | 253 | minor | minor by the corollary, patch by instinct |

Two of six were genuinely over-numbered. Not re-tagging them -- published tags
are immutable and the cost of a too-high number is only that it says less than it
could. The policy applies from here.

Opened 2026-07-10.
