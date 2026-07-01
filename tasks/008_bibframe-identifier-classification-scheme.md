# 008 -- Scheme fidelity for BIBFRAME identifiers and classification

## Context

The direct OverDrive provider (`ingest/overdrive/bibframe.go`) maps the Thunder
JSON straight to BIBFRAME and now retains data the MARC detour dropped: BISAC
classification (16,246 `bf:Classification` quads over the QLL corpus), the
OverDrive title id, and the Reserve ID -- all as `bf:identifiedBy` /
`bf:classification` nodes.

But libcodex's `bibframe.Identifier` and `bibframe.Classification` structs carry
only `{Class, Value}` -- **no source/scheme**. So today:

- BISAC codes render as a bare `bf:Classification` with a `bf:classificationPortion`,
  with **no `bf:source "bisacsh"`** to say what scheme the code belongs to.
- The OverDrive title id and the Reserve ID both render as plain `bf:Identifier`
  with a value and nothing distinguishing them. The availability adapter
  (`tasks/004`) keys on the **Reserve ID** and needs to find it unambiguously.

## Needed

Depends on a libcodex change (`libcodex/tasks/037`): add a `Source` field to
`Identifier` and `Classification`, rendered as `bf:source` across all four
serializations (RDF/XML, JSON-LD, N-Triples/N-Quads, Turtle) with
`TestEncodersIsomorphic` kept green.

Then, in this repo:

- Set `Source: "bisacsh"` on BISAC classifications.
- Tag the OverDrive identifiers so the Reserve ID is recoverable -- e.g.
  `Source: "overdrive"` on the title id and a distinct source/class on the
  Reserve ID (the Thunder availability key). Revisit whether the Reserve ID
  belongs on the Instance as an identifier or is better modeled as an
  availability-adapter key held outside the feed grain (see `tasks/004`).

## Acceptance

- [ ] libcodex `Identifier`/`Classification` carry a source; `bf:source` emitted.
- [ ] BISAC nodes carry `bf:source "bisacsh"`.
- [ ] Reserve ID is unambiguously recoverable from a grain (or intentionally
      moved out of the feed grain per the availability model).
- [ ] Grain golden test updated.
