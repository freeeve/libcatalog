# 238 -- work clone drops every uncontrolled subject/genre heading and never re-mints grain-local nodes, so clones lose reader-facing facets and inherit an ancestor work's identity

Filed from libcat-e2e on 2026-07-09 (cross-repo ask).

Two defects with one root: `CloneGrain` skolemizes blank nodes but *drops* blank
heading nodes, and renames the Work and Instance IRIs but *not* the grain-local
skolem nodes it previously minted. Everything else about clone is exactly as
documented -- 19 of 24 probe assertions pass, including the ones that matter
most (born suppressed, all-editorial, identifiers / adminMetadata / holdings /
work-links / lcat-markers dropped, source untouched, 404 and 401 guards).

## Symptom

### 1. Cloning a MARC-derived record silently loses all its uncontrolled headings

`w0cfnsjg6micju` on the 8481 playground is an ordinary feed record. Cloned
through `POST /v1/works/{id}/clone`, source untouched, clone tombstoned after:

```
SOURCE w0cfnsjg6micju Tags: ["Beginning Reader.","Juvenile Fiction."]  Subjects: ["https://homosaurus.org/v4/homoit0001369"]
CLONE  whgav49p5mmsqa Tags: null                                       Subjects: ["https://homosaurus.org/v4/homoit0001369"]
clone tombstoned: true | source grain 153 -> 153 quads
```

The controlled subject IRI carries over. Both uncontrolled headings are gone.
Those `Tags` are reader-facing -- `project.go:854` is what puts them there, and
the OPAC facets on them.

Reproduced from a fresh copycat sentinel carrying `650 _0 $a Cats`,
`655 _7 $a Science fiction` and one controlled LCSH IRI (`ui/probe_clone.mjs`):

```
PASS C15  the source has both a controlled and a MARC-derived heading   blank: 2; controlled IRI: 1
PASS C16  the clone keeps controlled subject IRIs                       controlled headings in the clone: 1
PASS C17  the clone drops MARC-derived (blank) headings                 source headings total 3, clone 1
FAIL C23  the clone keeps the headings a reader can facet on            source projects tags ["Cats"] -> the clone projects []
FAIL C20  the Clone button keeps the headings too                       1 of the source's 3 headings survive
```

### 2. A clone of a clone carries its ancestor's work id

`CloneGrain` names skolems `#<newID>n<N>`. Those names are never re-minted on a
subsequent clone, because `rename` only ever holds the old Work IRI, the
`#…Instance` IRIs, and blank-node labels. So cloning a work that was itself
cloned leaves most of the new grain named after the older work:

```
FAIL C22  a clone of a clone re-mints the grain-local nodes
          9/26 quads still name #wmp0h7qtbitr34nN, e.g. #wmp0h7qtbitr34n1
```

On a real, already-skolemized record (`w1ufqrjr57m2ie`) the leak is most of the
grain -- including the clone's **title node**:

```
clone -> 201 w737naa6eb0nug
clone quads: 101   quads still naming the SOURCE work id: 67
  LEAK <#ip21bgdl5d0sb6Instance> <…/bibframe/title>   <#w1ufqrjr57m2ien6>  <editorial:> .
  LEAK <#ip21bgdl5d0sb6Instance> <…/bibframe/extent>  <#w1ufqrjr57m2ien2>  <editorial:> .
       <#w737naa6eb0nugWork>     <…/bibframe/subject> <#w1ufqrjr57m2ien12> <editorial:> .
source untouched: 102 quads
```

The Work and Instance IRIs are freshly minted; two thirds of the statements
still say `w1ufqrjr57m2ie`. It compounds: every descendant of a clone keeps the
first work's id forever.

### 3. Nothing tells the cataloger

```
FAIL C21  the button warns that headings will be dropped
          the Clone button's only affordance is title="copy into a new suppressed draft with fresh ids"
```

## Root cause

`bibframe/clone.go:23-36`:

```go
case bfSubjectIRI, "http://id.loc.gov/ontologies/bibframe/genreForm":
    return q.O.IsBlank()
```

justified at `clone.go:18-21`:

> Blank headings must not skolemize (an IRI object of bf:subject reads as a
> controlled term everywhere) and blank editorial statements are unpatchable,
> so like identifiers they stay with the source

**Both halves of that reason are wrong, and the file says so itself 100 lines
later.** `clone.go:128-135`:

> `GrainLocalIRI` reports whether an IRI value is grain-local -- a fragment node
> the grain itself minted (`#<id>Work`, `#<id>Instance`, an editor or clone
> skolem) … a `bf:subject` object that is a grain-local node **is an
> uncontrolled heading whose rdfs:label is the value, never a controlled term,
> exactly like the blank nodes it stands in for**.

That is not just a comment. Both consumers implement it, citing tasks/218:

- `project/project.go:854` -- `if s.IsIRI() && !bibframe.GrainLocalIRI(s.Value)`
  → controlled term; **else** the node's `rdfs:label` becomes a tag.
- `ingest/enrich.go:403` -- `if subj.IsBlank() || (subj.IsIRI() && bibframe.GrainLocalIRI(subj.Value))`
  → uncontrolled heading; else a subject IRI.

So a skolemized heading does *not* read as controlled anywhere. And the second
half of the reason -- "blank editorial statements are unpatchable" -- is an
argument *for* skolemizing, which is exactly what `clone.go:100-113` already
does to every other blank node, for exactly that reason:

> a blank title/contribution node would make those fields uneditable -- the
> opposite of what a clone is for

Headings are dropped to avoid a hazard that skolemizing removes.

The second defect lives in the same `rename` map (`clone.go:67-80`): seeded with
`oldWork → WorkIRI(newID)` plus one entry per `#…Instance` subject, and grown
only for blank nodes at `clone.go:106-113`. A pre-existing `#<oldID>n<N>` is an
IRI and not a blank, so `renameTerm` (`clone.go:139-147`) passes it through
untouched.

## Why it matters

**Uncontrolled headings are most of the subject access in a MARC catalog.** For
the large majority of feed records the 650/655 headings arrive as blank nodes,
unlinked to any authority. Cloning a record to catalog a new edition is the
commonest way to make one, and it silently discards all of them. The cataloger
cannot notice: `GET /doc` never surfaces blank headings, `GET /marc` does not
render them back into 650/655 (the tasks/230 display asymmetry), and the Clone
button promises only "fresh ids". The loss surfaces later, to a *reader*, as
missing facets.

Losing the controlled IRIs would at least be loud -- they show in the editor.
Losing the uncontrolled ones is silent, which is worse.

**The identity leak breaks the function's own contract.** `CloneGrain`'s doc
says "the work and every instance are re-minted". Two thirds of the statements
in a second-generation clone still carry the first work's id, including the node
holding its title. Nothing is corrupted today -- grains are separate blobs,
merge is marker-based, and `mergedView` unions graphs only within a single grain
-- but `GrainLocalIRI` means "a node *this grain* minted", and after a clone of
a clone that is false. Anything that ever reasons across two grains (a diff
view, a dedup pass, a future physical merge) inherits a name collision that
stays invisible until it fires.

## Expected

One rule fixes both: **re-mint every grain-local node; skolemize every blank.**

- Seed `rename` with each grain-local IRI (`GrainLocalIRI(v)`) that is not
  already the Work or an Instance, mapping it to a fresh `#<newID>n<N>`.
- Drop the `bfSubjectIRI` / `genreForm` case from `cloneDropsSubgraph` and let
  heading nodes skolemize with everything else. `project.go:854` and
  `enrich.go:403` already read the result correctly, and the `rdfs:label`
  statement rides along on the renamed node.
- Fix the comment at `clone.go:18-21`, which asserts the opposite of
  `GrainLocalIRI`'s contract.
- If headings must still be dropped, say so on the button: "copy into a new
  suppressed draft with fresh ids -- subject and genre headings are not copied".

Identifiers, adminMetadata, holdings, work-to-work links and lcat markers should
keep being dropped. Those are right, and the probe asserts them.

## Repro

```
cd ~/libcat-e2e && node ui/probe_clone.mjs
```

Expect `C20`, `C21`, `C22` and `C23` to flip to PASS, with `C0`-`C19` staying
green -- in particular `C3`, `C4`, `C5` (the drops that *are* correct), `C6`
(born suppressed), `C7` (all-editorial), `C9` (no source refs), `C12` (source
untouched) and `C13`/`C14` (the 404 and 401 guards) must not regress. The probe
mints its own copycat sentinels, removes the relation and attachment it creates,
and tombstones every work and clone it makes. `harness/retest.mjs` carries the
same check as `t238`.

Note for whoever fixes this: `C22` is what distinguishes a real fix from a
cosmetic one. Renaming only the heading nodes would leave the title node still
named after the ancestor.
