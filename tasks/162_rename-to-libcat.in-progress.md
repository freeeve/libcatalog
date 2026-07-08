# 162: Rename project libcatalog -> libcat

Rename the project, repo, and module paths from `libcatalog` to `libcat`.
The binaries (`lcat`, `lcatd`) already carry the short name; this makes the
project name match them.

Scope:

- Module paths: `github.com/freeeve/libcatalog{,/backend,/hugo,/hugo/exampleSite}`
  -> `github.com/freeeve/libcat{...}` across all go.mod files and imports.
- Prose/config: README, docs/, CLAUDE.md, hugo module docs, exampleSite config,
  package.json names.
- `tasks/` history is left untouched on purpose -- done task files are records.
- GitHub repo rename `freeeve/libcatalog` -> `freeeve/libcat` (old URL redirects),
  origin remote update, then a lockstep v0.25.0 release under the new module path
  via scripts/release.sh (old tags stay on the old path; consumers of
  `github.com/freeeve/libcatalog@<=v0.24.0` keep resolving via the GitHub redirect).
- Local: `~/libcatalog` -> `~/libcat`, `~/libcatalog-playground` -> `~/libcat-playground`.
- qllpoc consumes the sibling checkout path (`LIBCATALOG ?= ../libcatalog` in its
  Makefile) -- filed as an uncommitted task in that repo per convention.
