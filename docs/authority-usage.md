# Authority usage

`lcat authorities` reports how heavily each **controlled subject authority** is
used across the whole corpus: for every authority any work is subject-linked to,
the count of distinct works that reference it. It answers a *cross-work*
question the per-work grain scans and the identity index cannot -- which
headings carry the collection, which are used by a single work and worth
reviewing, how coverage breaks down by scheme.

```sh
# the most-used headings first
lcat authorities catalog.nq --top 20

# single-use LCSH headings -- candidates for review or consolidation
lcat authorities catalog.nq --scheme lcsh --max-works 1

# everything used by at least 10 works, as json for a dashboard
lcat authorities catalog.nq --min-works 10 --format json
```

The dataset is a `catalog.nq` (the `lcat serialize` output). Flags: `--scheme`
limits to one vocabulary (`lcsh`, `fast`, `homosaurus`, ...); `--min-works` /
`--max-works` bound the inclusive usage window (`--max-works 1` isolates
single-use headings); `--top N` keeps the N most-used after filtering;
`--format text|json`; `--out <file>`. The text report states how many
authorities the corpus holds and how many the filter kept, so a scoped report
says what it left out.

## What counts as an authority

Only **controlled** subjects are tallied -- those whose URI resolves to a
recognized authority scheme (LCSH, FAST, Homosaurus, LC children's subjects, the
local authority namespace). Bare-label topics (no URI) and per-grain local
subjects (document-relative `#...` fragment IRIs) are omitted: they carry no
shared identity to aggregate across works, which is the whole point of the
tally. A heading a single work asserts more than once -- e.g. in both a feed
graph and the editorial graph -- counts once for that work.

Agents are deliberately **not** modelled here. In the graph a subject authority
is an IRI shared by every work that uses it, but an agent is a blank node scoped
to its grain (a MARC feed mints a fresh one per contribution), so two works by
the same author do not share an agent node. Author co-occurrence stays a
string-keyed concern of the "more like this" similarity layer, not a graph
query.

## How it works

The command loads the dataset into an in-memory
[gochickpeas](https://github.com/freeeve/gochickpeas) graph via the
`catalogindex` package -- a read-only, rebuildable analytics view of the
catalog. Blob grains remain the source of truth; this view is derived and
disposable, rebuilt from a fresh `catalog.nq` when the catalog changes rather
than mutated in place.

This is the read side of the catalog-index seam. An incremental *write-side*
index (upsert one changed grain, reproject only affected works) is a separate,
deferred concern -- the whole-corpus reproject remains the write path until a
collection is large enough to need otherwise.
