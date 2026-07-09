# 230 -- marc view does not re-render bf:date into 008/07-10 (display asymmetry, from 228 note)

Opened 2026-07-09. From libcat-e2e's closing note on tasks/228.

Their measurement: a builder edit of 008 "Date 1" writes the six typed
quads (bf:date "1999", bf:place, provisionActivity node) into the
grain, but `GET /v1/works/{id}/marc` reconstructs an 008 whose 07-10
still read blank -- the encode direction (008 bytes -> typed
properties) is not mirrored by decode (typed properties -> 008 bytes).
docs/marc-fidelity.md lists 008 as Kept, which is true for round-trips
of MARC-INGESTED records (the verbatim sidecar reproduces the original
008); the gap is editor-authored provision dates on records whose
sidecar predates the edit, or born-editorial records with no sidecar.

Diagnose which layer owns it: libcodex's decode builds the 008 --
check whether it derives 07-10 from bf:provisionActivity/bf:date at
all, or only from admin metadata. Likely a libcodex ask; file it with
the shape of the fix (decode 008/06-17 from the typed properties when
no verbatim sidecar field is present).

## Outcome

Diagnosed; owned by libcodex; filed as their tasks/103.

Mechanism: encode reads 008/07-10 as the provision-date fallback and
008/35-37 into Instance.Languages, but decode's reconstruction
(`control008Country`, reader_crosswalk.go:1078) deliberately mirrors
ONLY the country into 15-17 -- the date renders into 260 $c and the
language into no 008 position. Confirmed live on the playground: a
record with provision date "2010" decodes to an 008 blank at 07-10
with the date in 260 $c. Nothing is lost -- it is positional, not
semantic -- but the 008 builder appears to discard a saved date.

The ask (libcodex 103): mirror the provision date into 07-10 (+ "s"
at 06) when it is a plain 4-digit year, and the first language into
35-37 -- derivations from properties encode itself created, the exact
shape of the existing country mirror. No libcat code change needed;
the MARC view fixes itself on their release. docs/marc-fidelity.md
now carries the caveat on the 008 row until then (v0.81.0).
