# 065 -- Shortcuts on every screen; duplicates in nav; help polish

## Context

Final phase of the admin UX overhaul (tasks/059-064). Dashboard, Exports, Macros, and
Promotions had no keyboard support, Duplicates was reachable only through the palette,
and the `?` overlay mixed screen and global bindings into one flat list.

## Scope

- Dashboard: single letters `w a q b m e i u p` jump to every screen (mirrors the global
  `g` sequences).
- Exports: `j/k` select a job, `Enter` downloads it, `r` refreshes now, `n` focuses the
  new-export form; selected row carries the accent rail.
- Macros: `j/k` select, `Enter` edits (own macros), `n` starts a new macro.
- Promotions: `j/k` over pending proposals, `a`/`r` decide (librarian).
- Duplicates joins the top nav.
- Bindings carry their scope; the `?` overlay groups "This screen" / "Everywhere".

## Acceptance

- Every routed screen shows a live legend with at least its own keys.
- check/test/build green; axe suite green.
