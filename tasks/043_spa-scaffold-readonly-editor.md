# 043 -- SPA scaffold + read-only editor

## Context

The cataloger app is a Svelte 5 + Vite SPA in `backend/ui/`, ported from
qllpoc's review-app patterns (typed api.ts, PKCE auth.ts, keyboard.ts), served
by `go:embed` from the API binary (no CORS, one deployable; a deployment may
instead push dist/ to a CDN). The app boots from `GET /config` (API base,
issuers, vocab sources, profiles, branding tokens) so the framework ships zero
deployment specifics.

## Scope

1. `backend/ui/` scaffold: Vite + Svelte 5 + TS; `lib/{api,auth,keyboard,types,
   stores}.ts` (auth.ts supports both PKCE-to-external-issuer and local login).
2. Screens: Dashboard, WorkSearch (over projected catalog.json or a works
   listing endpoint), read-only WorkEditor native tab rendering the WorkDoc via
   ProfileForm with ProvenanceBadge per field.
3. `go:embed` of built dist/ in lcatd + `GET /config`; dev proxy config.
4. Keyboard scope stack + `?` help overlay foundations.

## Acceptance

- Login (local or OIDC) -> browse works -> open a Work read-only with
  provenance badges.
- JS unit tests for api/auth stores; a11y audit harness (axe over built UI)
  wired like the Hugo module's.
- `node_modules`/dist gitignored appropriately; build documented.
