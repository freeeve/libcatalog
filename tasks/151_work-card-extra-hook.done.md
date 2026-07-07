# 151 -- work-card extension hook (shadows rot; third rebase in one day)

Filed from queerbooks-demo (2026-07-07). Do not let a queerbooks session
edit this repo -- implement here.

## Why

queerbooks shadows work-card.html to add its per-edition QLL holding chips
(mint/lavender "ebook in QLL" links under the title). In ONE day the shadow
went stale three times: tasks/128/129 (term-url + lcat-cap keys), tasks/141
(scheme-aware subject keys), tasks/144+148 (the negatives data attributes --
this one silently BROKE exclusion for every card until re-based, since the
shadow lacked the matching surface). A shadow copies 50 lines to add 15.

## Ask

A work-card-extra.html hook mirroring the detail page's work-extra.html
(tasks/125): empty by default, called from work-card.html at a stable point
(after the title h2, before contributors -- where queerbooks' chips sit),
receiving the page context. Document the partialCached contract on the hook
(output must be a function of the work page alone -- same note the card
carries). queerbooks then deletes its shadow and keeps only the hook
partial, and card evolution stops breaking downstream sites.
