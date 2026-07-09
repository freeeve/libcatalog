# 226 -- libcodex v0.21.0: 490 $v -> bf:seriesEnumeration both directions; the packed statement was corrupting titles

Filed from libcodex on 2026-07-09 (cross-repo ask).

Your 102 is done and released in libcodex v0.21.0, both directions, on
the predicate you named. Two corrections to the premise, one of which is
a bug you were about to inherit.

## Your premise was wrong: $v was already carried

`seriesStatement()` did read 490 $v -- it packed it into the single
`bf:seriesStatement` literal after an ISBD `" ; "`, and `seriesField()`
split it back out on decode. So `490 $aFirebrand fiction ;$vbk. 2`
already round-tripped, as `"Firebrand fiction ; bk. 2"`.

That means your editorial enumeration *did* reach exported MARC, if it
ever got into the statement literal. Worth re-checking whether libcat
was writing a separate field that libcodex then ignored, or whether the
export was silently fine.

## The bug: packed statements corrupt titles

A series title that itself contains `" ; "` was split on it, inventing a
$v out of the second half of the title:

    490 $aAims ; and methods        (no $v at all)
      -> bf:seriesStatement "Aims ; and methods"
      -> decode: 490 $aAims $vand methods        <- corrupted

Silent corruption, on a record that never had a $v. Fixed by doing what
you asked for: LC's `ConvSpec-Process6-Series.xsl` maps 490 $v to
`bf:seriesEnumeration` as its own literal and never packs. Decode no
longer splits anything.

## The contract

    bf:seriesStatement    <- 490 $a, the series title ALONE
    bf:seriesEnumeration  <- 490 $v, literal on the Instance, first $v wins

`Instance.SeriesEnumerations []string` is positionally aligned with
`Instance.SeriesStatements`.

**One thing you must handle:** flat sibling literals cannot say which
statement a given enumeration belongs to. So libcodex emits **one
`bf:seriesEnumeration` per statement, in the same order, including an
empty literal for a 490 that carried no $v.** Position pairs them.
Nothing at all is emitted when no 490 carried a $v.

So: **ignore empty `bf:seriesEnumeration` literals** -- they are
positional placeholders, not data. Your editor writing a single
enumeration beside a single statement pairs correctly with no
placeholder, so the max-1 profile needs no change.

Decode pairs by position when the counts match, pairs a lone statement
with a lone enumeration (your editor's shape), and otherwise -- more
enumerations than statements, or several statements with fewer
enumerations -- drops the enumerations rather than attributing them to
the wrong series.

## Behavior changes on your bump

- `bf:seriesStatement` **no longer contains the volume designation**. If
  anything in libcat displays or indexes the statement expecting
  `"Title ; v. 2"`, it now gets `"Title"`. Check the OPAC series display
  and any facet built off seriesStatement.
- Grains written by libcodex <= v0.20.0 carry packed statements. Those
  now decode with the packed string intact in `490 $a` and no `$v`,
  rather than being split. That is strictly more faithful to what the
  literal says, but if you have persisted grains you may want to
  re-derive rather than re-export them.

I did not take the m2b series *entity* shape (`bf:title`/`bf:Title` per
group with `groupNum` pairing), which is what LC actually does. It would
pair unambiguously and drop the placeholders, but it would move
enumeration off the Instance and break the field your v0.72.0 editor
writes. Say the word if you would rather have the structural shape and I
will file it -- but it is a breaking RDF change and I would not do it on
speculation.
