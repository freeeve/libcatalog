# 291 -- lcat export emits sequential _:bN blank-node labels, so catalog.nq.gz and its published sha256 change on every release even when the catalog is identical -- grains already use canonical _:c14nN

Filed from queerbooks-demo on 2026-07-10 (cross-repo ask).

## What we saw

Adopting v0.116.0 over v0.114.0, with the corpus untouched:

- all 62,602 grains re-ingested **byte-identical**
- `catalog.json` re-projected **byte-identical**
- `catalog.mrc.gz` and `catalog.xml.gz` **byte-identical**
- `catalog.nq.gz` **changed**

The nq change is a pure blank-node relabeling. Both dumps have 5,458,350
statements and 1,093,632 blank nodes; erasing `_:b\d+` makes the sorted files
hash-identical, and the multiset of per-node signatures (sorted predicate+object
per blank subject) and the degree multiset both match exactly. Same graph, new
names.

`lcat export` run twice at a fixed version is deterministic, so this is not
map-iteration flakiness within a build -- the label assignment just follows a
traversal order that any code change can perturb.

## Why it matters

Grains already carry canonical labels:

    data/out/data/works/61/wod3d74r058vra.nq   ->  _:c14n0, _:c14n1, …
    site/static/downloads/catalog.nq.gz        ->  _:b1, _:b10, _:b100, …

The export drops that canonicalization. `downloads.json` publishes a sha256 per
file:

    {"name":"catalog.nq.gz","bytes":60826172,"sha256":"b4cd816a…","records":62602}

So every release republishes a 60MB dump with a new checksum for a catalog that
did not change. Anyone mirroring the download, diffing it, or pinning the hash
sees a spurious change; an S3 sync re-uploads it. The mrc and xml dumps, which
have no blank nodes to label, are already stable across the same bump -- so the
churn is specifically the nq serializer's.

## Ask

Emit canonical blank-node labels from `lcat export` -- the same `_:c14nN`
canonicalization the grain writer already uses -- so an unchanged catalog
exports byte-identically across releases, and the manifest's sha256 changes only
when the data does.

If full canonicalization over 1.1M blank nodes is too costly at export time, a
cheaper fix that still holds: assign labels in a deterministic order derived from
the grain (work id, then the grain's own `_:c14nN`), rather than from the
traversal.
