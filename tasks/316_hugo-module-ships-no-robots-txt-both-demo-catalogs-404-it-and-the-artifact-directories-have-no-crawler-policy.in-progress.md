# 316 -- hugo module ships no robots.txt: both demo catalogs 404 it, and the artifact directories have no crawler policy

Opened 2026-07-10. Split out of tasks/278, which measured it and explicitly
declined to fix it there: *"Not this task's bug -- a note, because crawler policy
is the other half of the artifact-enumeration story and it lives in the build, not
the server."*

## What was measured

Both published catalogs 404 `/robots.txt` (`:8482` and `:8502`, libcat-e2e's
`probe_opac_dirlist.mjs`, check D3).

tasks/278 stopped `lcat serve` from *enumerating* `/search/` -- 125 files, ~33MB
including a 9.9MB record store and a 6.2MB trigram index -- so a crawler can no
longer walk the directory. It can still fetch any of those files by name if it
learns the name some other way, and nothing tells it not to. Nothing in
`sitemap.xml` points at `/search/`, so today the listing was the only thing putting
it in front of a crawler; that is a reason to think this is now low urgency, not a
reason to think it is handled.

## The shape of the fix

`robots.txt` lives in the build, not the server, and Hugo already has the machinery:

- A site sets `enableRobotsTXT = true` and Hugo renders `layouts/robots.txt`.
- The module ships no such layout, so an adopter who enables the flag gets Hugo's
  built-in default (`User-agent: *` and nothing else) rather than one that knows
  where this catalog keeps its artifacts.

So: ship `hugo/layouts/robots.txt` that disallows the artifact directories
(`/search/`, `/lcat/`) and points at `sitemap.xml`, and document `enableRobotsTXT`
in the module README next to the SEO-head section. It cannot be turned on from
inside the module -- `enableRobotsTXT` is site config -- so the README has to say
so, the way the taxonomies block already does.

Do not put a `robots.txt` in the module's `static/`. It would silently override an
adopter's own, which is the opposite of what a module should do with a file a site
owner may have opinions about.

## Open question for the maintainer

Should the shipped default disallow `/search/` at all? The files under it are
fetched by `lcat-browse.js` on a normal page view, so a crawler executing JS pulls
them regardless, and `Disallow` would not stop that. The argument for it is bytes
and politeness; the argument against is that a `Disallow` on a path the site's own
JS fetches is a lie a crawler may hold against you. A `Crawl-delay`, or nothing at
all beyond the sitemap pointer, may be the honest answer.
