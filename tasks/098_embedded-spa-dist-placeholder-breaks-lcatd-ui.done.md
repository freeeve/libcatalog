# 098 -- lcatd serves a non-bootable SPA unless `ui/` is built before `go build`

> Filed from the libcatalog-demo repo (cross-repo note, uncommitted) while standing
> up the read-only cataloging demo (demo tasks/009). Left uncommitted so a session
> working in this repo owns it.

## Symptom

A plain `go build ./cmd/lcatd` (and `docker build -f backend/deploy/docker/Dockerfile`)
produces an `lcatd` whose **browser UI never boots**. `/config`, `/v1/*`, and auth all
work over the API, but loading `/` in a browser yields a blank page: the SPA's
`<script type="module" src="/assets/index-*.js">` is served as `text/html` (the
history-API fallback returns `index.html`), so the module is MIME-rejected and nothing
renders.

## Root cause

`backend/ui/ui.go` embeds `dist/` via `//go:embed all:dist`. Only
`backend/ui/dist/index.html` is git-tracked (a 439-byte **placeholder**); `dist/assets/`
is gitignored build output. The committed placeholder `index.html` references asset
hashes (e.g. `index-CQMg0ylv.js`) that do not match whatever is (or isn't) in a
developer's local `dist/assets/`. So the referenced JS/CSS 404 -> history fallback ->
`index.html` for the asset URL -> dead SPA. `ui.go`'s own comment documents the
contract: "`npm run build` (run before `go build` in a release) overwrites dist/ with
the real app." Nothing enforces it.

## Two gaps to close

1. **The release Dockerfile does not build the SPA.** `backend/deploy/docker/Dockerfile`
   jumps straight to `go build ./cmd/lcatd`, so the image embeds the placeholder. Add a
   node build stage that runs `npm ci && npm run build` in `backend/ui` and stage
   `dist/` into the Go build context before `go build`.
2. **A bare `go build` silently yields a broken UI.** Options: have `go generate`
   (or a Makefile target) build the SPA; fail fast at startup if the embedded
   `index.html` still looks like the placeholder; or ship a committed dist consistent
   with a committed index.html. At minimum, document the required `npm run build` step
   prominently in `backend/deploy/README.md`.

## Verified workaround (used by the demo)

Running `npm run build` in `backend/ui` (regenerates a consistent `dist/index.html` +
`dist/assets/`) then `go build ./cmd/lcatd` produces a working binary: the SPA boots,
`/assets/*.js` is served as `text/javascript`, sign-in works, and the read-only
dashboard renders (verified logged in as a bootstrap admin against a seeded grain dir,
102 works). The demo's own container image build will include the SPA build step, so
this is not a hard blocker for the demo -- but a stock `go build`/`docker build` of
lcatd shipping a broken UI is a footgun worth fixing upstream.

## Acceptance

- `docker build -f backend/deploy/docker/Dockerfile -t lcatd .` produces an image whose
  `/` boots the SPA in a browser (assets served with correct MIME types).
- A bare `go build ./cmd/lcatd` either builds the SPA too or fails loudly rather than
  silently embedding a placeholder.

## Resolved

- **Dockerfile** now has a `node` SPA-build stage that runs `npm ci && npm run build`
  in `backend/ui` and copies the result over the placeholder before `go build`, so the
  release image embeds a bootable UI.
- **The placeholder is now self-contained.** `backend/ui/dist/index.html` no longer
  references non-existent hashed assets; a bare `go build` serves a plain "UI not built
  -- run `npm run build`" page (no dead app shell), and `lcatd` **logs a startup
  warning** (`ui.IsPlaceholder()` detects the committed placeholder by a marker). The
  JSON API is unaffected either way.
- `backend/deploy/README.md` documents the manual `npm run build` step.
- Verified both directions: bare build -> warning logged + notice page at `/`; after
  `npm run build` -> no warning, real SPA boots, `/assets/*.js` served as
  `text/javascript`.
