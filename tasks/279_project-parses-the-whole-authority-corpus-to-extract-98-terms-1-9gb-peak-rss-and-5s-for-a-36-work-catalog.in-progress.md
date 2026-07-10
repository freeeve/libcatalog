# 279 -- project parses the whole authority corpus to extract 98 terms: 1.9GB peak RSS and 5s for a 36-work catalog

Opened 2026-07-09. Measured on the demo playground (36 works), macOS arm64,
`lcat` built at `backend/v0.111.0`.

`lcat project` reads `catalog.nq` whole, parses every quad into an in-memory
`rdf.Dataset`, and keeps the lot resident while it projects. The catalog contains
the works *and every authority grain the deployment has installed*, because
`bibframe.SerializeGrains` walks all `*.nq` beneath its `--dir`. On the playground
that is 434MB of LCSH/Homosaurus/FAST snapshots against 12MB of works.

The authority quads are not waste -- they are what mints the catalog's `terms`
registry, and the OPAC's subject pages need them. But **98 term records** come out
the far end, and the whole corpus is parsed and retained to produce them.

## Measured

```
$ /usr/bin/time -l lcat project --catalog site/data/catalog.nq \
      --provider copycat,marc,overdrive --out opac/assets
projected 36 works ...; facets: 2 languages, 18 subjects, 37 contributors
        5.00 real         4.51 user
  1861730304  maximum resident set size          <- 1.86 GB
 27465324179  instructions retired
```

`GODEBUG=gctrace=1` shows where it goes:

```
gc 1   264->264->263 MB     <- catalog.nq slurped whole (263MB file)
gc 2   638->638->638 MB     <- parsed dataset, 1.75M quads, all retained
gc 3  1376->1376->638 MB    <- projection churns ~740MB on top
gc 4  1362->1362->638 MB
```

A 263MB file becomes a 638MB live dataset (~365 bytes per retained quad), and
peak RSS is ~2.9x the live set.

The same grains with the authorities excluded (`serialize --dir site/data/works`):

```
catalog.nq: 9.5MB      project: 0.82 real, 81 MB peak RSS
```

**23x less memory, 6x faster -- and wrong.** The projected catalog drops from
**98 terms to 55**. The 43 that vanish are exactly the ones the authority grains
carry: `sh85077507 "Literature"` with its `broader`, `sh2008102054`
`"Divorce--Fiction"`, `gf2014026415`, and so on. `facets.json`'s `subjects` group
changes with them. Work count, every other facet group, and the redirect count are
identical, so nothing is lost but the vocabulary -- and the vocabulary is the
point of a subject page.

That is the shape of it: **the cheap path is the wrong one, and the correct path
pays for the entire corpus.**

## Why it matters

`tasks/085` measured memory as the wall for catalog scale. This is the same wall
reached from the other side, and much earlier: the cost is set by the size of the
installed *vocabulary*, not the catalog. A library with 36 works and LCSH pays
1.9GB. Adding works barely moves it; adding a vocabulary doubles it.

The writable-lambda deploy (`tasks/099`) and `lcat rebuild`'s incremental path
both run this code. A 1.9GB floor is a real constraint on where a build can run,
and it is paid on every rebuild.

## Expected

Neither extreme is right. The projection needs *the terms the works reference*,
not every term in every installed vocabulary -- and it can know which those are
before it needs their labels.

- **Two passes over the quad stream, one dataset.** Pass 1 reads the work grains
  and collects the term IRIs actually referenced (subjects, genre forms,
  classifications, contributors). Pass 2 streams the authority quads and keeps
  only those whose subject IRI is in that set, plus whatever `broader` closure the
  term pages walk. The 98 surviving records are ~0.006% of the corpus.
- **Do not slurp `catalog.nq`.** `gc 1` shows the whole 263MB file resident before
  a single quad is parsed. libcodex's `rdf` decoder streams; that `[]byte` should
  never exist.
- **Consider keeping authorities out of `catalog.nq` entirely.** They are a
  different kind of thing from a work grain, they change on a different cadence,
  and every consumer of the file pays to skip past them. A separate
  `authorities.nq`, or reading `data/authorities/` directly, would let `project`
  choose what to load. This is the larger change and probably the right one.
- **`SerializeGrains`'s doc comment is wrong regardless.** `--dir` is documented as
  "grain directory (holds `data/works/*.nq`)", and it walks every `*.nq` beneath
  the directory. A caller who believes the comment points it at `data/works` and
  silently ships a catalog whose subject headings have no labels. That is how this
  was found.

There is no `Benchmark` anywhere in `project/`, `index/`, or `ingest/`, so nothing
would catch a regression in any of this. One belongs here.

## Repro

```bash
cd ~/libcat-playground
# Freeze the grains first if lcatd is running: an in-flight write moves the counts.
cp -R site/data /tmp/frz

/usr/bin/time -l ./lcat project --catalog /tmp/frz/catalog.nq \
  --provider copycat,marc,overdrive --out /tmp/projA        # 1.9GB, 5.0s, 98 terms

./lcat serialize --dir /tmp/frz/works
/usr/bin/time -l ./lcat project --catalog /tmp/frz/works/catalog.nq \
  --provider copycat,marc,overdrive --out /tmp/projB        # 81MB, 0.8s, 55 terms

python3 -c "
import json
a=json.load(open('/tmp/projA/catalog.json')); b=json.load(open('/tmp/projB/catalog.json'))
ta={t['id'] for t in a['terms']}; tb={t['id'] for t in b['terms']}
print(len(ta), len(tb), 'lost:', len(ta - tb))"
```
