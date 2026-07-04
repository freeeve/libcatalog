# 081: Decode HTML entities in ingested MARC text

Work summaries in the playground corpus render literally as
`friendship&amp;#8212;a Newbery Honor Book` -- the source MARC carried
HTML-encoded (double-encoded) entities and ingest stored them verbatim, so
the editor and any public projection show `&amp;#8212;` instead of an
em-dash (spotted in the editor 2026-07-04; the text lives in the stored
`.nq` grains, e.g. `site/data/works/9d/w5h9o966r2nhos.nq`).

## Plan sketch

- Decide the decoding point: at ingest (marc provider text fields) so
  grains store clean text; existing grains need a reserialize/reproject or
  a batch cleanup.
- Decode numeric (`&#8212;` / `&#x2014;`) and named (`&amp;`, `&quot;`,
  &c.) entities, applied twice-safe for the double-encoded `&amp;#8212;`
  form; leave genuinely literal ampersands alone.
- Audit which fields are affected (summary at minimum; check title/notes).

## Resolution

Prose (520/505/5xx) was already handled by tasks/089 (`cleanFreeText` in the
MARC provider, decode-to-fixpoint + markup strip). The audit's open item was
titles, and it turned up two real residuals, both now fixed:

1. **MARC titles were uncleaned.** `cleanFreeText` covered prose but not
   transcribed titles (245/246) or the 245 $c responsibility statement.
   Extended to cover them.
2. **The OverDrive/Thunder provider decoded nothing at all.** Real Thunder
   titles carry references/markup (`LEGO&#174; Creations`,
   `Emperor Xuanzong of Qing&#8212;Min Ning`); the provider stored them
   verbatim. Now cleaned at the source (`overdrive.Provider.Records`), so both
   the BIBFRAME titles and the identity clustering key see clean text.

The decode/strip logic was promoted to a shared `ingest.CleanText`
(`ingest/textclean.go`), so both providers -- and any future one -- share one
fixpoint-safe cleaner. Headings and identifiers stay untouched; the MARC
verbatim sidecar still preserves original field bytes.

Tests: `ingest.TestCleanText` (table-driven), `marc.TestCleanFreeText`
(title assertions added), `overdrive.TestProviderCleansTitles`.

End-to-end verified against real data: re-ingesting a LAPL slice (which had
entity-laden titles) dropped title/subtitle literals carrying references/markup
from **2520 to 2** -- the two residuals are corrupt source fragments (truncated
`&#1` with binary garbage), which a decoder correctly leaves alone. Existing
stored grains self-heal on re-ingest (playground already shows 0).
