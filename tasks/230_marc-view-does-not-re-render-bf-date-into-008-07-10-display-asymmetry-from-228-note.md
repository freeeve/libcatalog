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
