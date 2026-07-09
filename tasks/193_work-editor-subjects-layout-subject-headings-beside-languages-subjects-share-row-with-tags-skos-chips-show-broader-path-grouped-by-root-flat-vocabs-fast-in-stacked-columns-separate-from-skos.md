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
