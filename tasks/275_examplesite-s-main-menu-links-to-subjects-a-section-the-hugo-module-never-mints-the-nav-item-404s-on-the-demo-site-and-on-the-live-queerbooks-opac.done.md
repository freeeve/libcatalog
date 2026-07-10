# 275 -- exampleSite's main menu links to /subjects/, a section the hugo module never mints -- the nav item 404s on the demo site and on the live queerbooks OPAC

Opened 2026-07-09.

## Outcome: NOT A BUG. Filed in error; the defect was in my own config.

Closed the same day it was opened. No code changed. Recording it rather than
deleting it, because the mistake is the kind worth not repeating.

`exampleSite` builds `/subjects/` correctly. Building it into a scratch dir
produces thirteen sections -- `classifications contributors en es formats
languages lists page sources subjects tags works` -- and `/subjects/` is
populated (`fast-transgender-people`, `homosaurus-gender-identity`, ...). The
menu link resolves.

**What actually happened.** I stood up a new OPAC for the demo playground
(`~/libcat-playground/opac`, now on `:8482`) and wrote its `hugo.toml` by hand,
omitting the `[taxonomies]` block. Hugo then fell back to its own
`categories`/`tags` defaults, no `subjects` taxonomy was declared, and the
`/subjects/` link I had copied from `exampleSite` 404'd. I read the 404 as
evidence about the module rather than about the config I had just written.

`hugo/hugo.toml:10-12` says this outright:

> NOTE: Hugo does not merge a module's `[taxonomies]` into the importing site's
> ... it is kept here as the canonical reference to copy.

An importing site must copy the block verbatim. `exampleSite/hugo.toml:74` and
`queerbooks-demo/site/hugo.toml:94` both do; my playground config did not.

**The corroborating evidence was also misread.** `curl localhost:8502/subjects/`
returns 404 on the live queerbooks OPAC, which I took as a second data point.
It is not: queerbooks declares the `subjects` taxonomy but its catalog mints no
`/subjects/` section, *and its menu never links there* -- `site/hugo.toml` links
only `/works/`. A path that 404s is not a dead link unless something points at
it. Nothing does.

**Fixed in the playground config, not in the repo.** `opac/hugo.toml` now carries
the `[taxonomies]` block and all eleven dimensions serve 200
(`/subjects/ /tags/ /formats/ /contributors/ /languages/ /classifications/
/sources/ /lists/ /works/`).

**The one thing here worth a real task**, if anything: a site that imports the
module and forgets `[taxonomies]` gets a silently degraded catalog -- facets
quietly absent, no warning, a build that exits 0. The module cannot merge the
table (Hugo's limitation), but it could *detect* the omission at build time and
`errorf`. That is a genuine papercut and it is what bit me. Not filing it
speculatively; noting it here so the next person who trips has the trail.
