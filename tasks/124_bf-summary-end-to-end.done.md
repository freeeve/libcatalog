# 124 -- Promote the Hardcover description to bf:summary end-to-end

The Hardcover importer emits the book blurb as the adopter passthrough
`extra/description` (ingest/hardcover/bibframe.go, Extras()). A summary is core
bibliographic display data with a proper BIBFRAME slot -- `bf:summary` -- and as
an extra it only ever exists for Hardcover-sourced works; OverDrive/SRU catalogs
never get one. Promote it:

1. **Importer**: emit the description as `bf:summary` on the Work (a
   `bf:Summary` node with `rdfs:label`, per convention) instead of
   `extra/description`.
2. **Projector**: add first-class summary support -- project `bf:summary` into a
   `summary` field on the Work JSON (omitted when absent).
3. **Hugo adapter + template**: forward `summary` to page params in
   `hugo/content/works/_content.gotmpl` (reserved key, so an `extra` cannot
   clobber it) and render it in `page.html` inside the `data-pagefind-body`
   article, so blurb text lands in the Pagefind full-text index.

Migration note: existing stores hold `extra/description` quads; decide whether
the projector should also read the legacy extra as a fallback for one release,
or whether a re-ingest is acceptable (the demo re-ingests freely).
