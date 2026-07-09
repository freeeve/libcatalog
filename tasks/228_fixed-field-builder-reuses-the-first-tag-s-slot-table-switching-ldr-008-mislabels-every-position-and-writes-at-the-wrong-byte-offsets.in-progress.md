# 228 -- fixed-field builder reuses the first tag's slot table: switching LDR<->008 mislabels every position and writes at the wrong byte offsets

Filed from libcat on 2026-07-09 (cross-repo ask).

## Symptom

In the MARC text surface, open the `LDR positions` builder, then click
`008 positions` without closing it. The builder now edits the 008 while still
showing the **leader's** slot table: every position is mislabeled, and every
edit lands at the leader's byte offsets inside the 008. The reverse order does
the same thing the other way.

Measured on the 8481 playground against a copycat-minted sentinel
(`ui/probe_fixedfield.mjs`):

```
PASS P2  CONTROL: 008 builder alone shows 008 slots     first slot: "Date entered (yymmdd) 00"
PASS P3  CONTROL: LDR builder alone shows leader slots  first slot: "Record status 05"

FAIL P4  switching LDR -> 008 shows the 008 slots       first slot now: "Record status 05"
FAIL P5  a builder edit writes at the slot it names     008 "               xx …"
                                                        ->  "     q         xx …"
                                                        typed "q" into the first slot; it landed at
                                                        offset 5. The 008's first slot is offset 0;
                                                        the leader's is offset 5.
FAIL P6  switching 008 -> LDR shows the leader slots    first slot on the LDR builder: "Date entered (yymmdd) 00"
                                                        leader "00000nam a2200000   4500"
                                                        ->     "970101am a2200000   4500"
```

P6 is the ugly one: typing a date into a slot the builder *calls* "Date entered"
overwrote the leader's record length, status, and type bytes.

The two controls (P2, P3) open each builder on a fresh mount and see the correct
slot table, which is what isolates this to the switch rather than to a bad table.

## Root cause

`backend/ui/src/components/FixedFieldGrid.svelte:21-22` reads the tag once, at
mount:

```js
// The tag is fixed for a mounted grid (the grid remounts per field row).
// svelte-ignore state_referenced_locally
const slots = fixedSlots(tag);
```

That assumption holds in `MarcGrid.svelte:197,233`, where each row owns an
instance inside an `{#each}`. It does **not** hold in
`MarcTextEditor.svelte:203-206`, which renders **one** instance for whichever
fixed line is open:

```svelte
{#if building !== null}
  {@const fl = fixedLines.find((x) => x.line === building)}
  {#if fl}
    <FixedFieldGrid tag={fl.tag} value={fl.value} onchange={(v) => applyFixed(fl.line, fl.tag, v)} />
  {/if}
{/if}
```

Clicking another `… positions` button sets `building` to that line
(`MarcTextEditor.svelte:198`) without ever passing through `building === null`,
so the component is never torn down. `tag` and `value` update; `slots` does not.
`applyFixed` then writes the value back to the *correct* line, using offsets
from the *wrong* table.

The same stale-`slots` hazard exists in `MarcGrid.svelte:233` if a cataloger
retypes a row's Tag input from `006` to `008` with that row's builder open.

## Why it matters

These bytes are not cosmetic. The 008 is converted to typed BIBFRAME properties
and persisted -- setting `Date 1 = 1999` through the *correct* builder writes
six quads including:

```
<#…Instance-marc-provisionActivity-…> <http://id.loc.gov/ontologies/bibframe/date> "1999" <editorial:> .
<#…Instance-marc-provisionActivity-…> <http://id.loc.gov/ontologies/bibframe/place> <…/countries/xx> …
```

So a wrong-offset write into 008/06-17 lands as a wrong publication date, date
type, or place of publication on the instance. The leader case (P6) rewrites the
record-status and record-type bytes that drive how the record is understood.

The mislabeling is the dangerous part: a cataloger reads "Date entered", types a
date, and the builder writes it somewhere else entirely. The raw line under the
grid does show the truth, but it shows it *after* the damage, and the labels
above it are actively lying.

## Expected

- The builder must not survive a tag change. Either key it --
  `{#key fl.tag}<FixedFieldGrid … />{/key}` in `MarcTextEditor.svelte:203` -- or
  make `slots` reactive: `const slots = $derived(fixedSlots(tag))`, which also
  fixes the `MarcGrid` row-retag case and lets the `svelte-ignore` come out.
- A component test: mount `FixedFieldGrid` with `tag="LDR"`, change the prop to
  `"008"`, assert the first slot's label becomes `Date entered (yymmdd)`.

## Repro

```
cd ~/libcat-e2e && node ui/probe_fixedfield.mjs
```

Expect `P4`, `P5` and `P6` to flip to PASS. `P7` is context, not an assertion on
the bug: it confirms 008 bytes reach the grain as typed properties. The probe
mints its own sentinel work and tombstones it; no pre-existing record is
touched. `harness/retest.mjs` carries the same check as `t228`.

## Note (not filed separately)

While confirming this, I measured that `GET /v1/works/{id}/marc` does **not**
render a saved `bf:date` back into 008/07-10 -- the quads are written, but the
reconstructed 008 still reads blank there. `docs/marc-fidelity.md:36` lists 008
as "Kept (survive MARC → BIBFRAME → MARC)". That looked at first like the
builder's edits being discarded; the grain diff proves they are not. Worth a
look, but it is a display asymmetry, not this bug.

## Outcome

Fixed in v0.78.0 (commit e16eadd), worked from the title (the filing's
detail had not landed yet; the title named the defect precisely).

`FixedFieldGrid` computed `fixedSlots(tag)` once at init, with a
comment asserting a mounted instance never changes tags. It does: an
in-place tag edit, and keyed lists (the mrk text editor's fixed panels
are keyed by line number) shifting which field lands on a surviving
instance. The stale table then mislabels every position and
`withSlotValue` writes runs at the previous tag's byte offsets. `slots`
is `$derived(fixedSlots(tag))` now.

Regression test mounts the component, flips the tag prop LDR -> 008
on the live instance, and asserts the slot table follows (Record
status gone, Language present). Full UI suite green (206).

If the pending detail names additional symptoms beyond the slot-table
staleness, reopen and we will pick them up.
