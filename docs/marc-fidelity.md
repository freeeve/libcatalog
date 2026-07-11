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
| 008 | Fixed-length data elements | since libcodex v0.9.0 -- reconstructed from typed properties (tasks/053). Positional parity since libcodex v0.22.0 (tasks/230, 235): the reconstruction mirrors the provision date into 06/07-10 when it is a bare four-digit year, the country into 15-17, and the first content language into 35-37 -- every position `FromRecord` reads back out. A date that is not a bare year (`c2010`, `2010-2012`), or two provisions naming different years, leaves 06/07-10 blank and lives only in 260 $c. |
| 020 | ISBN | qualifier text (e.g. `(electronic bk)`) rides in the value |
| 100 / 700 | Primary / added agent | 700 present on the audiobook sample |
| 245 | Title statement | |
| 250 | Edition statement | audiobook sample |
| 260 | Publication (AACR2) | |
| 300 | Physical description / extent | |
| 336 / 337 / 338 | Content / media / carrier type | 336 kept since libcodex v0.9.0; 337 partially collapses on multi-337 records |
| 306 | Playing time | since libcodex v0.11.0 (bf:duration) |
| 347 | Digital file characteristics | since libcodex v0.11.0 ($a/$b via bflc FileType/EncodingFormat; $2 not kept) |
| 490 | Series statement | since libcodex v0.25.0 one bf:relation per 490 on the Work (relationship/series -> bf:Series), so $a/$v pair per field; $x -> bf:Issn and ind1=1 -> mstatus/tr are carried. $n/$p, $l, $3 and the 880 parallel grouping are not. Before v0.25.0: flat bf:seriesStatement literals on the Instance, which lost the $a/$v pairing across repeated 490s |
| 500 | General note | since libcodex v0.9.0 (5XX -> bf:Note) |
| 511 / 521 / 533 / 538 | Specialized notes | since libcodex v0.11.0 -- typed bf:noteType decodes back to the original tag; note labels join every subfield |
| 520 | Summary | |
| 776 | Additional physical form | since libcodex v0.11.0 (the $c/$z print/ebook pairing survives as a bf:Isbn on the associated resource) |
| 650 | Topical subject | controlled (SKOS-shaped) subjects export as `650 _7 $a label $2 code $0 authority-iri` since tasks/136 -- see below |
| 655 | Genre/form | |
| 856 | Electronic location (access URL) | |
| 040 | Cataloging source | since libcodex v0.18.0 (tasks/192/194): `bf:AdminMetadata` models $a (`bf:assigner`), $b (`bf:descriptionLanguage`), $d (`bf:descriptionModifier`, one per agency), $e (`bf:descriptionConventions`); an internal marcKey `bf:Note` carries the field exactly, so $c survives too. See "Cataloging source" below for the derived export behavior. |

These are the identifiers, agents, title, publication, extent, carrier, summary,
subjects, genre, and access link -- the fields discovery is built on.

## Lost (do not survive the round-trip)

| Tag | Field | Why it is lost / where it lives instead |
|-----|-------|------------------------------------------|
| 037 | Source of acquisition / **Reserve ID** | **the OverDrive availability key** -- decodes as an 024-shaped identifier, not 037; the *direct* JSON→BIBFRAME path keeps it as a `bf:source`-tagged identifier (`tasks/008`) |
| 084 | Other classification (**BISAC** in MARC Express) | decodes to 072, not 084; the direct path keeps BISAC as a `bf:Classification` with `bf:source "bisacsh"` |

### Cataloging source (040): modeled, and derived at export (tasks/192)

Until libcodex v0.18.0 this table said "provenance is modeled as named
graphs, not a 040". That conflated two orthogonal axes: named graphs carry
**statement-level data provenance** (merging, overrides, the public-sources
allowlist), while 040 is **record-level cataloging-agency provenance** other
systems parse. Both are now carried:

- **Arrived 040s** are modeled (`bf:AdminMetadata` + the field-exact
  internal note) and decode back field-exact.
- **The deployment's own agency** joins at decode time, never in storage,
  so the field cannot drift from the graph: with a MARC organization code
  configured (`LCATD_ORG_CODE` for lcatd, `org-code` in `lcat.toml`'s
  `[export]` or `lcat export -org-code`), a record whose grain carries
  editorial statements gains the deployment as a trailing `$d` (modifying
  agency), and a record with no 040 at all -- the born-digital feeds --
  synthesizes `040 $a<org>$c<org>`. No code configured = no derivation.

### Relocated, not lost

- **041 (language code)** appears in the output though the samples carry language only
  in `008/35-37`: the crosswalk surfaces the coded language as an explicit `041`. Since
  libcodex v0.22.0 (tasks/235) the language is *also* mirrored back into `008/35-37`, so
  this is an addition rather than a move -- a decoded record carries the language in both
  homes, and `041 $h` (language of the original) never reaches the 008 slot.

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

The direct provider being preferable is **not** a claim that MARC Express is a
*poorer* record. Measured field-for-field (`tasks/085`, `tasks/092`), the Express
grain is the **richer** of the two OverDrive routes: ~125 quads/work vs ~65 for the
Thunder-JSON provider, and it carries a 520 summary, 5xx notes, LCGFT genre, a 490
series statement, 7xx added entries, extent/duration, and 856 locators that the
current Thunder crosswalk drops. That gap is a property of **our** Thunder crosswalk
(the JSON API carries most of this too, so the gap is closable by enriching the
direct provider), not evidence that MARC is the higher-fidelity *source*. The
routing decision stands on how each route *models* the two framework-critical fields
(037 Reserve ID, 084 BISAC), not on raw field count.

## What "Express" means (delivery speed, not record size)

"MARC Express" names the **delivery mechanism, not the record content** -- it is
express *delivery*, not express (i.e. stripped-down) *records*. Per OverDrive's own
[MARC Express](https://resources.overdrive.com/library/apps-features/marc-express/)
documentation, the service auto-generates "minimum MARC records" for free from
publisher-supplied metadata and delivers them "the day after you place a content
order" (managed under Admin → MARC Express deliveries in Marketplace; a backdated
file pulls the whole collection). The "Express" is the same-cycle, no-cost,
ready-to-load turnaround -- the historical pitch was "fast-turnaround MARC records"
([Library Journal, 2012](https://www.libraryjournal.com/story/overdrive-to-feature-fast-turnaround-marc-records-new-apis-for-opac-customization)).

By cataloging-industry standards Express *is* the lightweight tier -- OverDrive
positions it as a free "placeholder" and points libraries wanting full RDA/authority
records at paid third parties (OCLC, TLC eBiblioFile, BDS). Express records omit LC
and Dewey classification, the OCLC control number, and (as the loss table's
[uncontrolled-subjects note](#uncontrolled-subjects--tags-empty-subject-facet-is-expected)
records) authority-linked `6xx $0` subject URIs. So the precise statement is: MARC
Express is **richer than the current Thunder-JSON crosswalk output, lighter than a
full OCLC/LC catalog record** -- and "Express" refers to neither, only to how fast
and how freely the file arrives. OverDrive ships no "fuller" MARC tier of its own;
Express is the lightweight sibling of *third-party* full records, not of a premium
OverDrive feed.

## Controlled subjects in MARC output (tasks/136)

The ingest emission writes a controlled subject as `<work> bf:subject
<authority-iri>` plus the IRI's `skos:prefLabel` -- a SKOS shape the libcodex
crosswalk (which reads `rdf:type` Topic/Place/... + `rdfs:label` +
`bf:source`) does not see, so these subjects used to vanish from
`DecodeGrainMARC` output entirely. `DecodeGrainMARC` now shims them at decode
time: each such node gains (in memory only, never in the stored grain)
`rdf:type bf:Topic`, an `rdfs:label` from the preferred label (English
first), and a `bf:source` naming the thesaurus when the IRI belongs to a
known authority (homosaurus, FAST, LCSH), then the decoded heading gains
`$0 <authority-iri>` -- yielding `650 _7 $a Label $2 code $0 iri`.

Choices to know about:

- **`skos:broader` maps to nothing.** MARC `$x` subdivisions are a different
  axis than thesaurus hierarchy; flattening broader-chains into subdivisions
  would fabricate headings no authority defines.
- **Everything shims as `bf:Topic` (650).** The emission carries no
  Topic/Place/Person distinction; geographic or name authorities would need
  type info at ingest first.
- **Re-ingesting the `$0` link** (so a round-trip through MARC keeps the
  authority connection) is the libcodex `FromRecord` side -- tracked as a
  libcodex task; until then `$0` is output-only.

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
