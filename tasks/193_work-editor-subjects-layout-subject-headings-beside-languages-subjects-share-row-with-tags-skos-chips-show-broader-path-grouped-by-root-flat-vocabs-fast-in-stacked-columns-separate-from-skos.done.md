# 193 -- work editor subjects layout: subject headings beside languages, subjects share row with tags, SKOS chips show broader path grouped by root, flat vocabs (FAST) in stacked columns separate from SKOS

Opened 2026-07-09.

Eve's design direction (2026-07-09, from a work-editor screenshot with
a long single-column FAST subject stack and a mostly empty left
column):

- "There's a lot of open space here" -- Subject headings should sit
  next to Languages (currently Subject Headings is stranded below a
  tall empty Language cell while Subjects runs one chip per row down
  the right).
- Subjects can share space with Tags.
- "Do more with SKOS": show each SKOS term's full path (broader chain),
  in groups by root, perhaps. (Homosaurus terms have hierarchy; the
  work-search rail already distinguishes SKOS vs flat schemes,
  tasks/176, and SubjectNeighborhood has broader-chain data via
  /v1/terms/resolve.)
- Flat controlled vocabs (FAST) can stack in multiple columns; keep
  them separate from the SKOS group since they aren't hierarchy.

Scope: ProfileForm.svelte / WorkEditor layout only -- display and
arrangement, no data-model change. Subjects keep their scheme badge,
coll provenance badge, and Remove affordance. Screenshot reference in
the session around 2026-07-09; grain example: FAST x17 + homosaurus x3
+ overdrive subject headings + "audiobook in QLL"-style coll headings.

## Outcome

Shipped in 800a4ca, released v0.46.0; verified on the playground's
River of teeth (the subjects-rich work).

- Language | Subject headings share the first row; Subjects | Tags
  share a full-width row below (subjects flexible, tags a fixed rail)
  -- the empty left column is gone.
- SKOS terms group under their ROOT ancestor with the full broader
  path on each chip ("Life sciences › Biology › … › Gender expression
  › Gender nonconformity"); ancestors resolve breadth-first through
  /v1/terms/resolve (one batched call per level, cycle-safe, depth 12
  like the projector). First parent wins when a term has several.
- Flat schemes (FAST, wikidata) render apart from the SKOS groups as
  compact column stacks (column-width driven, hoodrow spans all).
  Chips keep the neighborhood disclosure, scheme badge, provenance,
  and Remove/undo affordances in every arrangement.
- Assumption per your "hold only for design" rule: root-group headers
  show the root term's label + scheme badge; single-term chains group
  under themselves. Adjust copy/thresholds cheaply if it reads wrong.
- Verification surfaced a pre-existing crash on multi-feed clustered
  works -- split off and fixed as tasks/196 (duplicate instance doc
  entries + per-graph value rows now merged with stacked badges).
