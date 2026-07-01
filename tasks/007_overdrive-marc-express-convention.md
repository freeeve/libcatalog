# 007 -- Reconcile the OverDrive connector with real MARC Express conventions

## Context

We built `ingest/overdrive` to crosswalk the cached OverDrive Thunder JSON into
MARC, then feed `bibframe.BuildMARC`. The field placement was inferred from
`qllpoc/docs/bibframe-field-mapping.md`, not from OverDrive's own MARC output.

OverDrive *does* publish and ship a real MARC convention (MARC Express, free
auto-generated records delivered to Marketplace). We downloaded their official
sample records and vendored them:

- `ingest/overdrive/testdata/marc-express/od-sample-audiobook.mrc` (15 records)
- `ingest/overdrive/testdata/marc-express/od-sample-ebook.mrc`
- Source: https://resources.overdrive.com/library/apps-features/marc-express/
  (also PDF field guides on static.od-cdn.com; the PDFs are image-only).

## Where OverDrive actually puts things (from the real samples)

| Data | Our writer today | OverDrive MARC Express | Action |
|------|------------------|------------------------|--------|
| Reserve ID | `024 8_ $a` | **`037 $a` + `$b OverDrive, Inc.` `$n http://www.overdrive.com`** | move to 037 |
| OD title id | `001` = numeric id; `024 7_ $a $2 overdrive` | `001` = `ODN0000######`; titleID only in `856 $u` | ours is clearer; keep, but note divergence |
| ISBN | `020 $a` (plain) | `020 $a` with `(electronic bk)` / `(sound recording)` qualifier | optional qualifier |
| BISAC | `072 _7 $a $2 bisacsh` | **`084 $a…$a…$2 bisacsh`** (repeated `$a`, one field) | move to 084 |
| Subjects | `650 _4 $a` | **`650 _7 $a…$2 OverDrive`** (named source) | set ind2=7 + `$2 OverDrive` |
| Author | `100 1_ $a $e $4` | `100 1_ $a` (name + trailing period only) | ours richer -- keep |
| Narrator | `700 1_ $a $e $4 nrt` | `700 1_ $a` **plus** `511 0_ $aNarrator: …` | ours richer -- keep; consider 511 |
| Title | `245 $a $b` | `245 $a $h[electronic resource] $b $c` | GMD `$h` is deprecated (RDA) -- keep omitting; could add `$c` |
| Publisher/date | `264 _1 $b $c` | `260 $a $b $c` (AACR2) | 264 is modern RDA -- keep |
| Content/media/carrier | `336 / 338` | `336 / 337 / 338 / 347` | ours minimal -- fine |
| Language | `041 $a` | coded in `008/35-37`; **no 041** in samples | keep 041 (explicit) |
| Genre/form | (none) | `655 _7 $aElectronic books.$2local` | optional |
| Summary / access | description is a sidecar | `520` summary; `538` access; `856 40 $u` link | different model (availability/desc handled elsewhere) |

## The real conclusion

OverDrive MARC Express is a **lossy, AACR2-flavored re-encoding of the same JSON**
we already hold (deprecated GMD `$h`, `260` not `264`, subjects as uncontrolled
`$2 OverDrive` strings, no authority control). It is **not** a higher-fidelity
source than the Thunder JSON -- so routing OverDrive through MARC buys nothing for
fidelity. This is the "contrived source file" smell, confirmed.

Split the concern (ARCHITECTURE §9):

1. **OverDrive reference provider -> map Thunder JSON directly to BIBFRAME**
   (`feed:overdrive`). No MARC. Keeps BISAC as classification, per-format ISBNs as
   distinct Instances, and subjects without MARC's constraints.
2. **MARC-import provider (the ILS ramp)** must *read* real MARC Express: Reserve
   ID from `037 $a`, BISAC from `084`, subjects from `650 _7`. The vendored samples
   are its fixtures / golden tests.

If we keep the `overdrive -> .mrc` writer at all, it should only exist as a
faithful MARC Express *stand-in* for exercising path 2 -- so align it to the table
above (037, 084, 650 _7 $2 OverDrive) or delete it in favor of path 1.

## Acceptance

- [ ] Decide: direct JSON->BIBFRAME provider vs. keep the MARC writer.
- [ ] If MARC writer stays: emit 037 (reserve id), 084 (BISAC), 650 _7 $2 OverDrive.
- [ ] MARC-import provider reads 037/084/650 correctly; golden test against the
      vendored `testdata/marc-express/*.mrc`.
- [ ] `tasks/003` known-loss updated with the JSON->MARC->BIBFRAME lossiness
      demonstrated here.
