# 318 -- the admin header never wraps, so every screen scrolls horizontally below 1342px and Sign out is off-screen on a phone

Filed from libcat-e2e on 2026-07-10 (cross-repo ask).

## Symptom

The SPA has a hard minimum width of **1342px**. Below it, *every* screen scrolls
horizontally -- not one screen, all thirteen -- and the number does not move,
because nothing in the header reflows.

Measured on `:8481`, signed in as an admin
(`harness/probe_admin_layout.mjs`, 6/8):

```
viewport   document.scrollWidth   horizontal scrollbar
  1600            1600                   no
  1440            1440                   no
  1366            1366                   no
  1280            1342                   YES
  1024            1342                   YES
   768            1342                   YES
   375            1342                   YES
```

Bisected: it fits from **1342px** up and scrolls at 1341px and below.

`1280x800` and `1024x768` are ordinary laptop and iPad resolutions. At 1280 a
cataloger gets a horizontal scrollbar on every screen in the product.

At a phone width the header does not merely overflow, it carries the session
controls away with it. At 375px:

```
8 of the 12 nav links are off-screen   (Macros, Exports, Import, Duplicates,
                                        Withdrawals, Queue, Promotions, Profiles)
"Dark mode"  starts at x=1179   (viewport is 375)
"Sign out"   starts at x=1271
```

Sign out and the theme toggle are reachable only by scrolling ~1000px to the
right, past a nav bar that gives no hint anything is there.

## Root cause

Two flex rows with no `flex-wrap`, and no media query anywhere in `App.svelte`.

```css
/* App.svelte:266-271 */
.top { display: flex; align-items: baseline; gap: 1.25rem; padding: 0.8rem 1.5rem; … }

/* App.svelte:287-291 */
nav  { display: flex; gap: 1rem; flex: 1; }
```

`flex-wrap` defaults to `nowrap`, so the header's width is its min-content width
-- brand + 12 nav links + `.side` (identity, *Dark mode*, *Sign out*) -- and it
never gets smaller. `grep -n "@media" App.svelte` returns **nothing**;
`app.css:102`'s only layout breakpoint governs `.split`, not the header.

Confirmed causally, at 375px, by two independent interventions:

```
                       as-shipped   header hidden   header + flex-wrap:wrap
/                         1342           375                375
/works                    1342           538                538
/queue                    1342           375                375
/profiles                 1342           397                397
```

Hiding the header and letting the header wrap produce **identical** results, so
the header is the whole of the 1342px floor, and `flex-wrap: wrap` is the whole
of the fix.

## Outcome

Shipped in **libcat v0.141.4** (patch -- corrects a layout bug, nothing to adopt
beyond rebuild/restart). Took the fix the bisection pointed at: `flex-wrap` on
all three header rows.

`App.svelte`:

- `.top` gets `flex-wrap: wrap` + `row-gap: 0.5rem` so the session controls
  (`.side`) drop to their own line instead of setting a one-line min-width.
- `nav` gets `flex-wrap: wrap`, `min-width: 0`, and a row/column gap so the 11-12
  links wrap rather than acting as a rigid min-content block.
- `.side` gets `flex-wrap: wrap` so the identity, theme toggle, and Sign out wrap
  on a phone instead of overflowing.

Desktop is unchanged: at 1600px the brand and the session controls still share
one row (no premature wrap).

### Verified end to end on :8481 (Playwright)

Causal proof, mirroring the task's own bisection: at each viewport the page's
`scrollWidth` with the header present equals its `scrollWidth` with the header
removed -- so the header contributes **nothing** to the floor.

```
             with header   header removed
/(dash) 375       375            375
/works  375       538            538   <- content min-width, not the header
/queue  375       375            375
/profiles 375     397            397   <- content min-width, not the header
```

All 16 (4 screens x 1280/1024/768/375) match. The residual 538/397 on works and
profiles at 375px are those screens' **content** min-widths -- they equal the
task's own "header + flex-wrap:wrap" column exactly, confirming the header is out
of the picture; narrowing that content is a separate per-screen concern.

At 375px, **Sign out** now sits at x=24..124 and the theme toggle ends at x=273,
both fully inside the viewport (was ~1271 and ~1179 off-screen). `npm run check`
clean; full UI suite 323/323.

The link count is not a constant. `tasks/244` made the nav derive from the one
`SCREENS` table (`App.svelte:172`, `sidebarScreens(canAdmin($sessionStore))`),
which is the right design -- and it means **every screen added to `screens.ts`
widens the header**, and the threshold moves with the signed-in role: an admin
sees 12 links, a librarian 11. Nobody will notice the day it crosses their own
monitor's width.

The codebase already knows the idiom: `grep -rn "flex-wrap" backend/ui/src`
returns **ten** uses, in `BatchOps`, `Queue`, `Promotions`, `Profiles` and
`VocabSources`. The one flex row that never wraps is the global one every screen
inherits.

## Why it matters

Responsiveness is this app's stated intent, not an ambition I am importing:

- `backend/ui/index.html:5` -- `<meta name="viewport" content="width=device-width, initial-scale=1.0">`
- `ProfileForm.svelte:879` and `VocabPicker.svelte:311` ship `@media (max-width: 40rem)` --
  rules written for a **640px** viewport
- plus breakpoints at 52rem, 60rem, 72rem, and 1100px

A rule written for `max-width: 40rem` is a promise that 40rem works. Those rules
are unreachable in practice: the header forces 1342px before they can matter, so
the app's own phone styling has never been seen doing its job.

It is also a **WCAG 2.1 AA failure -- 1.4.10 Reflow**, which requires content to
reflow at 320px without two-dimensional scrolling. The stylesheet already holds
itself to AA (`app.css:279`, *"provenance inks brighten to hold AA on dark"*),
and `tasks/315` is the other half of that same bar.

And the practical edge: the two controls stranded furthest off-screen are *Sign
out* and the theme toggle. `tasks/223` went to some trouble to make a dead
session clear the header identity immediately -- on a narrow screen nobody can
see the header identity at all.

## Expected

`flex-wrap: wrap` on `.top` and on `nav`, which the measurement above shows is
sufficient to drop every screen to its content width. Beyond that, a real header
treatment for narrow viewports (a menu, or `overflow-x: auto` scoped to the nav
so the *page* does not scroll) is a design call, not a defect fix.

Two smaller overflows survive the header fix and are **not** this task, but are
worth their own look, since the numbers above expose them:

```
/works      538px   div.results-list   (a fixed-width result row)
/profiles   397px
```

A regression test belongs next to the a11y gate: assert
`document.documentElement.scrollWidth <= clientWidth` at 375px on every screen in
`SCREENS`. The check exists in `probe_admin_layout.mjs` as `L1`.

## Repro

```
cd ~/libcat-e2e && node harness/probe_admin_layout.mjs   # 6/8; C1 and L1 fail
node harness/retest.mjs                                   # t318
```

Screenshots: `shots/admin-narrow-works.png`, `shots/admin-1280-overflow.png`.

Note `C1` -- *"no overflow at a desktop width"* -- is written as a control and
**fails**, which is itself the finding: the overflow is not a narrow-viewport
bug, it is a 1342px floor that a 1280px laptop already sits below. `L2`-`L5`
pass: every screen has exactly one `<h1>`, no screen skips a heading level, every
screen exposes a `<main>`, and every interactive control has an accessible name.
The document structure is sound; only the geometry is not.
