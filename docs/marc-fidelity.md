# MARC ↔ BIBFRAME round-trip fidelity

MARC→BIBFRAME conversion is lossy in both directions -- LC's own converters drop
data, and BIBFRAME has no home for some MARC constructs. "No lossy intermediary"
(ARCHITECTURE §2) is about the graph-vs-markdown decision; it is **not** a claim of
round-trip fidelity. Adopters bringing an ILS's MARC judge the framework on exactly
this, so the loss is **measured and pinned**, not assumed away (`tasks/003`).

## How this is measured

`bibframe/roundtrip_test.go` round-trips the vendored OverDrive MARC Express samples
(`ingest/overdrive/testdata/marc-express/od-sample-{ebook,audiobook}.mrc`, 15 records
each -- real OverDrive output) through libcodex:

```
MARC record --Encode--> BIBFRAME (RDF) --Decode--> MARC record
```

and compares field-tag presence in vs out. Two CI gates guard it:

- **`TestMARCRoundTripCoreFieldsSurvive`** -- fails if any core bibliographic field is
  dropped. A fidelity regression breaks the build.
- **`TestMARCRoundTripNoUndocumentedLoss`** -- fails if the round-trip drops any field
  not listed below. A crosswalk change that quietly loses data breaks the build until
  it is measured and added here.

Update this table and `knownLostFields` in the test together when the crosswalk
changes.

## Kept (survive MARC → BIBFRAME → MARC)

| Tag | Field | Notes |
|-----|-------|-------|
| 001 | Control number | |
| 006 / 007 | Additional/physical coded elements | since libcodex v0.12.0 -- folded into media/carrier both ways (upstream tasks/082) |
| 008 | Fixed-length data elements | since libcodex v0.9.0 -- reconstructed from typed properties (tasks/053) |
| 020 | ISBN | qualifier text (e.g. `(electronic bk)`) rides in the value |
| 100 / 700 | Primary / added agent | 700 present on the audiobook sample |
| 245 | Title statement | |
| 250 | Edition statement | audiobook sample |
| 260 | Publication (AACR2) | |
| 300 | Physical description / extent | |
| 336 / 337 / 338 | Content / media / carrier type | 336 kept since libcodex v0.9.0; 337 partially collapses on multi-337 records |
| 306 | Playing time | since libcodex v0.11.0 (bf:duration) |
| 347 | Digital file characteristics | since libcodex v0.11.0 ($a/$b via bflc FileType/EncodingFormat; $2 not kept) |
| 490 | Series statement | since libcodex v0.11.0 (bf:seriesStatement; $v rejoins after " ; ") |
| 500 | General note | since libcodex v0.9.0 (5XX -> bf:Note) |
| 511 / 521 / 533 / 538 | Specialized notes | since libcodex v0.11.0 -- typed bf:noteType decodes back to the original tag; note labels join every subfield |
| 520 | Summary | |
| 776 | Additional physical form | since libcodex v0.11.0 (the $c/$z print/ebook pairing survives as a bf:Isbn on the associated resource) |
| 650 | Topical subject | |
| 655 | Genre/form | |
| 856 | Electronic location (access URL) | |

These are the identifiers, agents, title, publication, extent, carrier, summary,
subjects, genre, and access link -- the fields discovery is built on.

## Lost (do not survive the round-trip)

| Tag | Field | Why it is lost / where it lives instead |
|-----|-------|------------------------------------------|
| 037 | Source of acquisition / **Reserve ID** | **the OverDrive availability key** -- decodes as an 024-shaped identifier, not 037; the *direct* JSON→BIBFRAME path keeps it as a `bf:source`-tagged identifier (`tasks/008`) |
| 040 | Cataloging source | provenance is modeled as named graphs, not a 040 |
| 084 | Other classification (**BISAC** in MARC Express) | decodes to 072, not 084; the direct path keeps BISAC as a `bf:Classification` with `bf:source "bisacsh"` |

### Relocated, not lost

- **041 (language code)** appears in the output though the samples carry language only
  in `008/35-37`: the crosswalk surfaces the coded language as an explicit `041`. The
  information survives; its MARC home moves.

## The `lcat:marcVerbatim` sidecar (tasks/049)

Since tasks/049 the known-loss tags are **no longer dropped at MARC ingest**: each
record's known-loss fields (see `bibframe.KnownLoss`) are serialized field-exact
(tag + indicators + subfield runs) and stored as `lcat:marcVerbatim` literals on the
Instance node, in the feed graph. Consumers of `bibframe.DecodeGrainMARC` -- MARC
export and the MARC view -- re-attach them, so the original forms round-trip even
though the crosswalk models them differently. Edits to a lossy tag in the MARC view
land as editorial `lcat:marcVerbatim` statements with an `lcat:overrides` claim
shadowing the feed copies, the same tasks/042 semantics every other field edit gets.
The loss table remains the honest contract for what the *graph model* carries; the
sidecar is the guarantee that nothing is silently thrown away.

## Why this validates the OverDrive architecture

The two framework-critical MARC losses -- **037 (Reserve ID)** and **084 (BISAC)** --
are exactly the fields the OverDrive **direct JSON→BIBFRAME** provider preserves
(`ingest/overdrive`, `tasks/008`). This is the measured backing for the decision that
OverDrive ingests directly, and MARC is only the existing-ILS onboarding ramp
(`tasks/007`): the MARC detour would silently drop the availability key and the
subject classification. (With the tasks/049 sidecar, even the MARC ramp now carries
them verbatim -- the direct provider remains preferable because it *models* them.)

## Uncontrolled subjects → tags (empty subject facet is expected)

OverDrive MARC-Express 6XX subject fields carry **no `$0` authority URI** -- only
`$2` source vocabularies (chiefly `bisacsh`, which the crosswalk models as
classification). With no authority URI, the crosswalk emits each topical heading as
a labeled `bf:Topic` blank node (correct BIBFRAME for an uncontrolled term). The
projector then routes any authority-less `bf:subject` into the **tag** dimension,
not the controlled **subject** facet (`project.subjectsAndTags`). So a corpus built
purely from OverDrive -- via either the MARC-Express ramp or the direct Thunder-JSON
provider, both authority-less -- projects an **empty subject facet**; its topical
terms are present as tags. This is vendor data, not a crosswalk or projection defect:
controlled subject facets require authority-linked source records (e.g. an LCSH `650`
with `$0 http://id.loc.gov/authorities/subjects/sh…`).

## MODS / schema.org

ROADMAP Phase 0 also lists MODS and schema.org export. These are **export-only** in
scope: libcodex renders them from a record/BIBFRAME, but the framework does not import
or round-trip them, so no fidelity contract is claimed for them here. A round-trip gate
can be added the same way if an import path is ever introduced.
