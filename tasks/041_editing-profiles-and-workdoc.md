# 041 -- Editing profiles + WorkDoc mapper

## Context

MARC frameworks become **editing profiles**: JSON documents (SHACL-lite)
declaring which fields a form shows, their cardinality, datatype, value source,
and defaults. The WorkDoc mapper materializes a grain into the typed JSON
document the SPA edits and back, with a passthrough bucket so unclaimed quads
are never dropped. Pure Go -- can proceed in parallel with backend infra.

## Scope

1. `backend/profiles/`: profile schema `{id, label, resourceType: work|instance|
   item|authority, fields: [{path, predicate, label, help, min, max, datatype,
   valueSource: {kind: literal|langLiteral|date|enum|vocab|authority|entity,
   ref}, default, hidden, marcHint}]}`; shipped defaults (work-monograph,
   instance-ebook, instance-print, item, authority-topic, fastadd);
   `lcat profiles validate` subcommand (the Koha "framework test" equivalent:
   predicates resolve against the BIBFRAME/lcat ontology, defaults type-check).
2. `backend/editor/doc.go`: WorkDoc `{workId, etag, profileId, work fields,
   instances[], items[], relationships[], passthrough[]}`; every field value
   carries `{v, prov: "feed:<p>"|"editorial", overridden}` (overridden wired
   fully in tasks/042). grain -> doc -> grain round-trip.

## Acceptance

- Golden round-trips against existing testdata grains: doc -> grain reproduces
  the input byte-for-byte when nothing is edited (passthrough proves lossless).
- Profile validation catches unknown predicates and bad defaults.
- Profiles are deployment-overridable JSON-in-repo (documented).
