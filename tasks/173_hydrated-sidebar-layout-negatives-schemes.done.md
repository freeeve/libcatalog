# 173: Hydrated sidebar (170) -- layout overflow, missing negatives, flattened schemes

Left by the queerbooks-demo session 2026-07-08 (uncommitted cross-repo note).
Found by Eve within minutes of browsing the 170 sidebar; screenshot-level
symptoms on the works list at ~63k works.

## Done (2026-07-08, v0.31.0)

1. **Overflow**: shrink/wrap rules now key to the value span, not the row
   markup (`.lcat-facets .lcat-facet-value { flex:1 1 auto; min-width:0;
   overflow-wrap:anywhere }`), covering linked, hydrated, and panel rows;
   qbd's interim qb-theme.css patch can drop.
2. **Negatives in browse mode**: hydration unhides each row's shipped-hidden
   `.lcat-facet-not` as an aria-pressed exclude toggle (i18n strings reused
   from lcat-negatives-config); `selected()` emits pressed rows as
   `{field, category, exclude:true}` -- the reader subtracts those posting
   sets natively. Include/exclude on one row are mutually exclusive.
3. **Schemes**: two fixes. The fallback panel no longer renders over a shared
   fragment still in flight (that race is what showed the flat "SUBJECT"
   panel); and when the panel legitimately renders, it now groups subjects
   per [params.subjectSchemes] with localized labels from a new
   `browse-subjects.json` sidecar (search.BuildBrowse; id -> labels+scheme;
   absent sidecar degrades to the old flat panel).

Verified end-to-end in Chromium (hugo/e2e, 17 checks incl. new negatives +
scheme-grouping coverage, zero console errors); jsdom sidebar tests and
search package tests pass.

## 1. Facet rows overflow the sidebar into the results column

The static rows' shrink rules are keyed to `li > label`
(`.lcat-facets li > label .lcat-facet-value { flex: 1 1 auto; min-width: 0 }`),
and the hydrated rows' markup doesn't match the selector -- long labels
("Gender-nonconforming people") plus counts overflow the fixed 16rem grid
track and paint over the result titles. Fix: apply the flex/min-width/
overflow-wrap treatment to hydrated rows too (or make the rules
markup-agnostic: `.lcat-facets li { display:flex } .lcat-facet-value
{ flex:1 1 auto; min-width:0; overflow-wrap:anywhere }`).

queerbooks-demo carries exactly that as an interim patch in its site theme
(qb-theme.css, marked with this task number) -- lift it if useful; we drop
ours when this lands.

## 2. No negative filters in browse mode

`[params.facets] negatives = true` (144) gives term pages the exclude
affordance, and the shared-fragment rows even render the hidden
`.lcat-facet-not` buttons -- but the hydrated sidebar never surfaces them,
and lcat-browse.js's `selected()` only collects positive [field, cat]
pairs. Eve's ask, verbatim: "it would be nice to have the negative filters
on the facets so I can remove segments of results." Needs the exclude
toggle on hydrated rows plus reader-side exclusion (subtract the excluded
categories' posting sets from the result ids).

## 3. Subject schemes flattened into one group

The static sidebar honors [params.subjectSchemes] (141): Homosaurus and
FAST as separate groups. The hydrated panel renders one "SUBJECT" group, so
same-label concepts from both schemes show as confusing duplicates
("Lesbians 1615 / Lesbians 1517", "Gay men 1288 / 1139", ...). Group the
hydrated facets by the configured schemes like the static rail does.
