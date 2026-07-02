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

- [ ] A clustered ebook+audiobook Work exposes both formats via its Instances.
- [ ] `catalog.json` Instances carry `format`; the format facet filters correctly.
- [ ] Re-ingest stays byte-stable.

Blocked on: libcodex `tasks/039`.
