# 120 -- .lcat-btn component: solid / surface / ghost variants, AA in both modes

Filed from libcatalog-demo. The module ships no button styles, so every adopter
invents CTA buttons on top of the tokens -- and the token pairs are easy to get
wrong across light/dark. The demo just shipped a real bug from exactly this
(its tasks/016): a `.evl-hero a { color: var(--lcat-on-accent) }` rule beat the
button variant's own color by specificity, rendering white-on-white in light mode
and near-black-on-near-black in dark.

Proposal: a small `.lcat-btn` block in lcat.css owning the three obvious variants
with mode-correct pairs (values from the demo's `assets/lcat-theme.css`, in
production at libcatalog.evefreeman.com):

- `--solid`: background --lcat-accent / color --lcat-on-accent,
- `--surface` (demo calls it --light): background --lcat-surface / color
  --lcat-accent / border --lcat-border,
- `--ghost`: transparent / color+border --lcat-on-accent (for accent-filled
  surfaces like a hero).

Because the module would own both the tokens and the component, the AA pairing is
verified once (the module's axe tooling) instead of per-adopter. The demo would
then drop its .evl-btn rules and keep only spacing tweaks.

## Status (2026-07-05 session)

Done. `.lcat-btn` block in lcat.css with the three variants on the module's
own token pairs, values as proposed (demo's --light renamed --surface):
--solid (accent/on-accent), --surface (surface/accent/border), --ghost
(transparent, on-accent text+border, for accent-filled surfaces). Base rule
works on <a> and <button> (font: inherit, cursor), hover underline,
focus-visible from the module's global rule. Documented in the README
theming section with the pairing table and the both-modes caveat on
--lcat-on-accent. The demo can drop its .evl-btn rules (keep spacing tweaks)
once it maps --light -> --surface.
