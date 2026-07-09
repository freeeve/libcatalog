# 235 -- libcodex v0.22.0: reconstructed 008 now mirrors date (06/07-10) and language (35-37), not just country

Filed from libcodex on 2026-07-09 (cross-repo ask).

Your 103 is done and released in libcodex v0.22.0, exactly as specified.
Your fidelity-doc caveat can drop.

## What landed

`control008` (decode) now renders every position `FromRecord` reads out
of an 008, at the same derive-don't-fabricate confidence the country
already had:

    06/07-10  a provision's bf:date / bflc:simpleDate, when it is a bare
              four-digit year; 06 = 's'
    15-17     the controlled bf:place country IRI      (unchanged)
    35-37     the Work's first content language

The 260 $c still carries the date too -- it legitimately lives in both,
per your note.

Verified against the shape you reported: a record with provision date
2010, place nyu, language eng now decodes to

    008 "      s2010    nyu                 eng  "
    260 $aAshland $bBlackstone $c2010

and re-encoding that decoded record reads country nyu, date 2010,
language [eng] straight back out. Your 008 builder will no longer look
like it discarded the edit.

## Boundaries, so you know what to expect

- **Not a bare year -> blank.** `"c2010"`, `"2010-2012"`,
  `"2010 printing"` stay in the 260 $c and leave 06/07-10 blank. No
  parsing heroics, as you asked.
- **`"[2010]"` DOES mirror.** This one is counterintuitive and worth
  knowing: `FromRecord`'s `cleanDate` already strips brackets, so a
  transcribed `[2010]` reaches the graph as the bare year `2010`. It is
  therefore a derivation, not a parse, and it lands in 07-10. Pinned by
  a test so nobody "fixes" it later.
- **Disagreeing provisions assert nothing.** Two provisions naming
  different years (publication 2001, manufacture 2005) leave 06 and
  07-10 blank -- the reconstruction cannot say which one the 008 meant.
  Two provisions agreeing on one year are not ambiguous and do mirror.
- **35-37 is the content language, never 041 $h.** A language of the
  original is skipped; that slot does not hold it.
- **No 008 at all** when the graph names none of date, country or
  language. Nothing is fabricated.

## On your bump

Purely additive to what decode emits. A reconstructed 008 that was
previously blank at 06, 07-10 and 35-37 may now be populated. No API
change.

If anything in libcat asserts on the *blankness* of those positions --
a snapshot test of exported MARC, say -- it will need updating. Worth a
grep before you bump; the workindex snapshot compare is the likely
place.

## Outcome

Adopted in **v0.86.0**. Bumped root and backend to libcodex v0.22.0; both
suites green with no test churn -- their warning about snapshot assertions on
008 blankness turned out not to apply. Nothing in libcat asserted on those
positions; the only 008 assertion is `copycat/templates_test.go` checking the
template's length is 40, which is unaffected.

Confirmed live on the exact record from tasks/230:

    008 "      s2010    nyu                 eng  "
    260 $a Ashland $b Blackstone Audio… $c 2010

06 = `s`, 07-10 = `2010`, 15-17 = `nyu`, 35-37 = `eng`, and the 260 $c still
carries the date. The fixed-field builder no longer looks like it discarded a
saved edit -- which was the whole complaint behind e2e's 228 footnote.

### The gate

`TestMARCRoundTrip008PositionsSurvive` pins the parity *positionally*, which
the three existing round-trip gates structurally cannot: they compare field-tag
presence, and the 008 was never absent -- only hollow. The new test encodes and
decodes each vendored MARC Express sample and compares the date, country and
language slots, asserting nothing where the input slot is blank or the date is
not a bare year (their documented boundaries: `c2010`, `2010-2012`, and
disagreeing provisions stay in 260 $c).

**Verified to fail against v0.21.0** rather than assumed to bite -- it
reproduces tasks/230's finding exactly:

    008 date 07-10 = "    " after the round trip, want "2005"
     in: "140607s2005    nyu     s     000 0 eng d"
    out: "               nyu                      "

Worth recording, because it nearly produced a false green: `go.work` unifies
the root and backend modules, so MVS picks the **maximum** libcodex
requirement across the workspace. Downgrading only the root module left the
build on v0.22.0 and the new gate passed vacuously. Both modules must be
downgraded to test against an older dependency.

### Docs

`docs/marc-fidelity.md`: the 008 row's tasks/230 caveat is replaced by the
positional-parity statement plus the boundaries. The "Relocated, not lost" note
on 041 was also stale -- language now lands in both 041 and 008/35-37, so it is
an addition rather than a move, and `041 $h` (language of the original) never
reaches the 008 slot.

Closes the loop opened by tasks/230 -> libcodex tasks/103.
