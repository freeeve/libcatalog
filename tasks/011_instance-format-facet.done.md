# 011 -- Per-Instance format, so the format facet is correct

## Problem

Format (ebook vs audiobook) is currently modeled as the **Work** content class
(`bf:Audio` / `bf:Text`), set from the *first* clustered item's OverDrive type
(`ingest/overdrive/bibframe.go`, `workClass`). When editions cluster -- the whole
point of Phase 1 -- an ebook + audiobook collapse into one Work with a single
class, so the per-edition format distinction is lost. The "one Work page with
**format facets**" goal (ARCHITECTURE §4) needs format at the **Instance** level.

## Scope

1. **libcodex** (dependency, see its `tasks/039`): give `bibframe.Instance` a
   carrier/format field so a caller can express ebook vs audiobook per Instance
   (BIBFRAME `bf:media` / `bf:carrier` on the Instance).
2. **libcatalog `ingest/overdrive`**: map the OverDrive item type to the Instance
   carrier (not, or in addition to, the Work class). Decide whether the Work still
   carries a content class at all once formats live on Instances.
3. **libcatalog `project`**: add `format` to the projected `Instance` and surface a
   `formats` facet on the Work (union of its Instances' formats).
4. **`tasks/009`** consumes the format facet.

## Acceptance

- [x] A clustered ebook+audiobook Work exposes both formats via its Instances.
- [x] `catalog.json` Instances carry `format`; the format facet filters correctly.
- [x] Re-ingest stays byte-stable.

Was blocked on libcodex `tasks/039` -- shipped in libcodex v0.7.0 (libcatalog
`tasks/013`), which unblocked this.

## Done (commits `fa13b01` project/provider, `6f273c1` Hugo)

Format is now a per-Instance property (RDA media type), not just the clustered Work
content class, so a Work realizing an ebook + an audiobook edition exposes both.

- **libcodex** (its `tasks/039`, in v0.7.0): `Instance.Media`/`Instance.Carrier`
  (`bf:media`/`bf:carrier`), each serialized as a labeled node. No libcatalog-side
  change beyond consuming it.
- **`ingest/overdrive`**: `Instance()` sets `Media` per RDA (`rdaMedia`: audiobook
  -> "audio", ebook -> "computer") and `Carrier` "online resource" (both digital).
  The Work `Class` is kept (valid for single-format Works) but is no longer the
  format-facet source.
- **`project`**: `Instance.Format` (from the Instance's `bf:media` label via a
  general RDA-media -> discovery-format map: audio->audiobook, computer->ebook,
  video->video, unmediated->print, else passthrough; carrier fallback for print);
  `Work.Formats` = the union of its Instances' formats; `Facets.Formats`.
  **SchemaVersion 3 -> 4.** The map is provider-agnostic (no OverDrive specifics).
  Search text unchanged (format is a filter facet, not free text).
- **`hugo`**: `format = "formats"` taxonomy; Work detail renders a Formats meta row
  (linked to `/formats/<f>/`) and per-edition format (`data-format` + a visible
  label); facet sidebar gains a Formats dimension. Module target -> v4; README +
  exampleSite updated (exampleSite Work 1 now clusters an ebook + audiobook).

Validated: unit tests (single-format + a clustered ebook+audiobook asserting both
formats + the facet); on the corpus a fresh ingest -> project yields schema v4 with a
format facet of **ebook 4,621 / audiobook 1,603** over 5,659 works -- **565 works
carry both formats** (e.g. "Imogen, Obviously": one audiobook + one ebook Instance),
and re-ingest with the new media triples is byte-stable (0 minted). Hugo exampleSite
builds on v4: `/formats/ebook/` lists both works, `/formats/audiobook/` lists one,
and the detail page shows each edition's format.
