# 227 -- MARC subfield syntax has no escape: a dollar amount in a subfield value silently reparses into a different subfield

Filed from libcat on 2026-07-09 (cross-repo ask).

## Outcome

Fixed in v0.77.0 (commit 29049ea) with the stricter delimiter rule
from the filing: "$"+code counts only when preceded by line start or
whitespace AND followed by whitespace or line end, so "$24.95" can
never be a delimiter. The serializer always emits the spaced form, so
real delimiters are unaffected; the deliberate cost is that "$aFoo"
(no space) reads as literal text. Both surfaces share the parser, so
grid and text are fixed together. The filing's three corruption cases
plus a round-trip property are pinned in marc.test.ts.

Residual, by the nature of the syntax rather than this bug: a value
containing a stand-alone " $b " sequence still reads as a delimiter --
no mnemonic MARC format distinguishes that without an escape, and
introducing "$$" would put escapes in front of catalogers for the
common case this task is about. The empty-value filter is retained
(an intentionally empty typed subfield is still dropped); with the new
rule the "$c $24.95" case that made it destructive now parses as data.

Verified with the filer's probe_marc_text.mjs against the rebuilt
8481: 8/8 including P4 (unrelated edit leaves the note intact), P5
(direct edit keeps the amount), P6 (no invented subfield).

## Symptom

A `500 $a List price $24.95 at issue` -- a plain note with a price in it --
loses the price the moment anything in the record is saved from the MARC tab.
The `$24.95` becomes `4.95`. No error is shown, no parse error is reported, and
the cataloger never touched the 500.

Measured on the 8481 playground against a copycat-minted sentinel work
(`ui/probe_marc_text.mjs`):

```
PASS P1  the server stores the note verbatim           500 as stored: "$a List price $24.95 at issue"
PASS P2  the grid shows the note it was given          500 line reads "$a List price $24.95 at issue"
PASS P3  the text buffer shows the note                500 line: "500    $a List price $24.95 at issue"
FAIL P4  an unrelated text edit leaves the note intact 500 was "$a List price $24.95 at issue"
                                                       ->  now "$a List price 4.95 at issue"
                                                       the buffer reported 0 parse errors
FAIL P5  editing the note keeps its dollar amount      500 now "$a List price 4.95 at publication"
```

`P4` only edited the **245**, in text mode. `P5` retyped the note's last two
words in the **grid**. Both surfaces destroy the price.

What the client actually sends, captured off the `POST /v1/works/{id}/marc`
request body:

```
POSTED 500 -> [{"code":"a","value":"List price"},{"code":"2","value":"4.95 at issue"}]
```

The note has been split into a `$a` and an invented `$2`. The `$2` is then
discarded by the 500's BIBFRAME conversion, so what lands in the grain is a
single `$a` reading `List price 4.95 at issue`.

## Root cause

`backend/ui/src/lib/marc.ts:18` splits a subfield line on `$` followed by one
alphanumeric, and nothing escapes a literal `$`:

```js
const re = /\$([a-z0-9])\s?/gi;   // "$24.95" matches as code "2", the \s? is optional
```

so `"$a List price $24.95 at issue"` yields `a="List price"`, `2="4.95 at issue"`
(and the now-empty `$c`-style entries are dropped by `.filter(sf => sf.value !== "")`
at `:30`). The inverse, `subfieldsToLine` at `marc.ts:8`, writes `$${code} ${value}`
with no escaping either, so the pair does not round-trip.

Both MARC editing surfaces are built on that pair:

- the grid's subfield line input (`MarcGrid.svelte:237`) reparses the edited row
- the mrk text buffer reparses **every** field on every keystroke
  (`mrk.ts:86`, `lineToSubfields(line.slice(7))`), which is why an edit to the
  245 corrupts the 500

`mrk.ts:1-6` states the invariant the code does not hold:

```
// ... subfield lines in exactly the grid's "$a … $b …" syntax (shared helpers),
// so grid and text are two views of one doc and round-trip losslessly.
```

Reproduced against the pure function alone, no browser:

```
"$a Foo $b Bar"           -> [{a,"Foo"},{b,"Bar"}]                 ok
"$a The novel $c $24.95"  -> [{a,"The novel"},{2,"4.95"}]          $c gone, $2 invented
"$a Item $c $5.00"        -> [{a,"Item"},{5,".00"}]                $c gone, $5 invented
```

## Why it matters

Dollar amounts in subfield values are ordinary cataloging, not an edge case:
`020 $c`, `037 $c`, `500`, `520`, and price notes generally. A cataloger opens a
record, fixes a typo in the title, saves, and a price three fields away is
quietly rewritten. Nothing in the UI reports it -- the text buffer says zero
parse errors, and the diff preview shows the mangled value as though the
cataloger had typed it.

Because the grid and the text surface share the helpers, there is no safe
surface: the corruption follows the record through both.

Worse for auditing: the change is attributed to the cataloger in the audit log
and the quad diff, since as far as the server can tell they typed it.

## Expected

- A literal `$` in a subfield value survives a grid or text-mode round trip.
  Either escape it on serialize (`$$`) and unescape on parse, or require a
  delimiter to be `$` + code + **space**, anchored at line start or after a
  space -- `$24.95` then cannot be a delimiter, since `$2` is followed by `4`.
  (The serializer already always emits `$a value` with the space, so the
  stricter rule costs nothing; it does drop support for typing `$aFoo`.)
- A round-trip property test: for any subfield value,
  `lineToSubfields(subfieldsToLine(sf)) === sf`. The three cases above are
  enough to fail today.
- If a value genuinely cannot be represented, the editor must say so rather
  than silently rewrite it.
- Consider whether `.filter(sf => sf.value !== "")` (`marc.ts:30`) should drop a
  subfield the cataloger typed; it currently erases `$c` here without a word.

## Repro

```
cd ~/libcat-e2e && node ui/probe_marc_text.mjs
```

Expect `P4`, `P5` and `P6` to pass. The probe mints its own sentinel work
carrying the note (a `500 $a`, because `020 $c` is dropped by the BIBFRAME
conversion long before the UI sees it) and tombstones it on the way out; no
pre-existing record is touched. `harness/retest.mjs` carries the same check as
`t227`.
