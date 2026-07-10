# 282 -- exports download uncompressed: gzip at rest, serve nquads/jsonld/marc as .gz and csv with Content-Encoding

Opened 2026-07-10, from Eve: "can we make the exports auto gzip before
downloading".

## What was true

`GET /v1/exports/{id}/download` wrote the stored bytes out as
`application/octet-stream`, and `export.Run` wrote them into the blob store raw.
Nothing in the server did response compression. Measured on the 31-work
playground, full-corpus:

| format | raw | gzip | ratio |
|---|---|---|---|
| nquads | 10,385,384 B | 527,213 B | **19.7x** |
| jsonld | 4,898,080 B | 197,053 B | **24.9x** |
| marc | 637,124 B | 91,328 B | 7.0x |
| csv | 6,607 B | 2,654 B | 2.5x |

Scaled to queerbooks' 62,602 works the N-Quads dump is on the order of a couple
of GB.

## The design fork

"Auto gzip" splits into two different products, and the difference is what lands
in the librarian's Downloads folder. Put to Eve:

- transparent `Content-Encoding: gzip` -- saves bandwidth only; a 2GB dump is
  still 2GB on disk;
- a real `.gz` artifact -- saves bandwidth *and* disk, but the CSV no longer
  opens by double-clicking.

**Eve chose the hybrid**: compressed at rest, `.gz` for the machine formats, and
CSV transparent because it is the human-facing format that goes into Excel and
OpenRefine.

## Outcome

Shipped in **v0.115.0** (`f4cf31b`).

| format | filename | Content-Type | Content-Encoding |
|---|---|---|---|
| `csv` | `<id>.csv` | `text/csv` | `gzip` |
| `nquads` | `<id>.nq.gz` | `application/gzip` | -- |
| `jsonld` | `<id>.jsonld.gz` | `application/gzip` | -- |
| `marc` | `<id>.mrc.gz` | `application/gzip` | -- |

### Where the decision had to live

Not in the handler. `DownloadURL` hands the browser a **presigned store URL**
when the blob store signs, and on that path none of our serving code runs -- the
object's own metadata is the entire contract. So `blob.PutOptions` gained
`ContentEncoding`, `blobs3` sets it on both `PutObject` paths, and
`export.DeliveryFor` is the single place that decides an export's path, type and
encoding. `Run` writes through it; the download handler reads it back.

That also means a `.gz` artifact must **not** carry `Content-Encoding: gzip`. It
is a file, not a transport encoding of a file, and a browser told otherwise
inflates the 2GB dump back on arrival. Mutation-proven below.

### Streaming preserved

The gzip writer sits inside the existing `io.Pipe`, so the emitters still write
per grain and the compressor's window is the only added memory (tasks/108's peak
memory property survives). On an aborted emit `gz.Close()` is skipped and the
emit error goes to the pipe, so `PutStream` fails and stores nothing.

### Backward compatibility

Exports written before this hold plain bytes. `Open` and `OpenStored` sniff the
RFC 1952 magic number rather than trusting the path, so old jobs still download
for their remaining TTL.

### Mutation-proven

| mutation | result |
|---|---|
| `gz.Close()` never called | 6 tests fail -- the object is a truncated gzip stream |
| always serve gzip, ignoring `Accept-Encoding` | 3 fail, incl. the pre-existing `TestExportBatchSelection` |
| give the `.gz` artifact `Content-Encoding: gzip` | 3 fail across both packages |
| `Vary: Accept-Encoding` removed | 1 fails |
| `gz.Close()` error swallowed (`if err == nil` dropped) | **passes** |

The last row is recorded because it is the one I asserted in a comment and could
not demonstrate. On a healthy pipe `Close` does not fail, and on a broken one
`PutStream` has already failed. The comment now says what the code does rather
than what I hoped it prevented.

`acceptsGzip` parses tokens rather than substring-matching, because `gzip;q=0` is
a refusal and `gzipped` is not consent. Both are in the table test.

### Verified live

Against the playground at v0.115.0, all four formats:

```
FORMAT  FILENAME                 CONTENT-TYPE      ENC    MAGIC  WIRE     IDENTITY
csv     27125dc1e1c7960b.csv     text/csv          gzip   1f8b     2643       6663
nquads  3c57aaf4265e3b3c.nq.gz   application/gzip  -      1f8b   577074     577074
jsonld  7a41a5e30b74fe86.jsonld.gz application/gzip -     1f8b   201642     201642
marc    0e24be9aeea1564d.mrc.gz  application/gzip  -      1f8b    96672      96672

CSV gzip wire form, inflated, vs the identity form: IDENTICAL
```

`WIRE` is with `Accept-Encoding: gzip`, `IDENTITY` is curl's default (no header).
Only CSV differs between them, which is the whole design.

### Adoption

Server plus a label change in the SPA. Rebuild and restart. This is a **minor**
release under `docs/versioning.md` because consumers have work to do:

- **Download filenames changed.** `<id>.nquads` is now `<id>.nq.gz`; `<id>.csv`
  is unchanged. Any script that saved by name must adjust.
- **The machine formats download gzipped.** A script that piped
  `GET .../download` into a MARC or N-Quads parser must now gunzip first. `curl
  --compressed` will *not* do it for them: those are `.gz` artifacts, not
  transport-encoded responses.
- **CSV is unchanged for every client**, gzip-accepting or not.
- `blob.PutOptions` gained `ContentEncoding`. Third-party `blob.Store`
  implementations compile unchanged (it is a new struct field) and may ignore it,
  exactly as they may ignore `ContentType`.

### Not done

Compressing the grain tree itself, or the vocabulary sidecars. Different
lifetimes, different access patterns, and grains are read far more often than
they are written.
