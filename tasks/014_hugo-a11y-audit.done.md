# 014 -- Formal accessibility audit of the Hugo module

## Problem

"Accessible catalog" is a stated goal (ARCHITECTURE §6/§7), but the Hugo module had
no formal a11y check -- only hand-written semantic markup. Verify it against WCAG and
make the check repeatable so regressions are caught.

## Done (commit pending)

Ran an axe-core (WCAG 2.1 A/AA) audit under jsdom over the built exampleSite (36
pages: home, `/works/`, Work detail, and every taxonomy + term page).

- **Finding + fix:** `heading-order` (WCAG 1.3.1/2.4.6) on result lists -- work cards
  used `<h3>` directly under the page `<h1>`, skipping `<h2>`. Changed
  `_partials/work-card.html` to `<h2>` (the correct next level under the page title;
  `.lcat-card-title` sets `font-size` explicitly, so the visual size is unchanged).
  After the fix: **0 violations across all 36 pages**.
- **Repeatable gate:** committed `hugo/a11y_audit.js` (walks a built site, runs
  axe-core per page, exits non-zero on any violation) + a dev-only `hugo/package.json`
  (`npm run test:a11y`, `npm run test:js`; `node_modules`/lockfile gitignored). Hugo
  ships only templates and assets -- this tooling is never consumed by a consuming
  site. README gains an Accessibility section.
- **Already sound (confirmed, no change needed):** skip link, `<main>`/`<aside>`/
  `<nav>`/`<header>` landmarks, labeled search input, `role="status"` result counts
  and availability placeholder (`aria-live="polite"`), `lang` on `<html>`, aria-labels
  on facet regions.

## Out of scope / follow-up

- **color-contrast** is excluded from the automated run: jsdom does no layout, so
  contrast can't be computed. The default palette is high-contrast on white, but a
  real-browser check (Lighthouse / axe DevTools) should confirm it -- and any theme
  overriding `assets/lcat.css` must re-verify. Noted in the README.
- Wire `test:a11y` + `test:js` into CI once the deployment repo has a JS toolchain.

## Acceptance

- [x] Automated WCAG 2.1 A/AA audit passes on every built page (contrast excepted).
- [x] The audit is committed and repeatable (`npm run test:a11y`).
- [x] Any violation found is fixed in the module templates.

## Refs

- `hugo/a11y_audit.js`, `hugo/package.json`, `hugo/layouts/_partials/work-card.html`,
  `hugo/README.md` (`## Accessibility`). Pairs with `tasks/004`'s `availability_test.cjs`.
