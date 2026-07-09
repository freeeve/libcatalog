# 192 -- derive exported 040 from graph provenance: org-code config, editorial-graph $d, flip the 040 fidelity row

Opened 2026-07-09.

Revisit of the docs/marc-fidelity.md row "040 | Cataloging source |
provenance is modeled as named graphs, not a 040" (Eve, 2026-07-09).
The two are orthogonal axes -- named graphs carry statement-level data
provenance (merging, overrides, public-source allowlists); 040 is the
record-level cataloging-agency chain other systems parse (OCLC dedup,
ILS quality rules). Decision: use both, with the 040 DERIVED from
graph facts at export -- never stored as an authoritative parallel
copy, so the field can't drift from the graph.

Blocked on libcodex tasks/094 (040 <-> bf:AdminMetadata: $a/$b/$c/$d
modeling in FromRecord + 040 regeneration on decode; $e already
modeled) and the corresponding libcodex release bump.

Scope here once that lands:

1. Org-code config: a MARC organization code for the deployment
   (lcat.toml / LCATD env, per the prefer-native-config-formats
   convention) identifying this catalog as an agency.
2. Export-side derivation in the DecodeGrainMARC re-attach layer:
   - record arrived with a 040 (feed graph AdminMetadata): emit it
     from the model; append our org code as $d when the editorial
     graph carries lcat:overrides statements for the work.
   - born-digital record (OverDrive JSON, coll feed -- no 040): 
     synthesize $a/$c = our org code, $e from the profile's
     conventions if declared.
3. Verbatim-sidecar handoff: once 040 is modeled, stop routing it
   through lcat:marcVerbatim (bibframe.KnownLoss) -- inbound bytes
   still win for fields the model can't represent.
4. Flip docs/marc-fidelity.md 040 row Lost -> Kept together with
   knownLostFields (the TestMARCRoundTripNoUndocumentedLoss gate
   enforces the pairing), documenting the derivation rule.
