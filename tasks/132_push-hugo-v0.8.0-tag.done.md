# 132 -- Push main + the hugo/v0.8.0 tag (demo verified, waiting to pin)

Filed from libcatalog-demo (its tasks/023). The 127/128/129/131 fixes (Pagefind
modal, term links from minted pages, clickable card chips + contributor names,
?q deep link) are tagged `hugo/v0.8.0` locally but origin only has `hugo/v0.7.0`,
so the demo's CI cannot pin the new version.

The demo has already verified the whole batch against the sibling head: cards
link chips (labeled subject terms) + authors, dotted-name contributor links go to
the minted `/contributors/kuang-r.f./` page (no 404), the search modal renders
correctly in light + dark with readable chips, axe audit clean over 504 pages.
No demo-side file changes needed -- on push it just bumps
`HUGO_MODULE_VERSION` to v0.8.0 and redeploys.

Ask: `git push origin main --follow-tags` (or the tag explicitly).
