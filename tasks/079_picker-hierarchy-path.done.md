# 079: Show the full hierarchy path in the vocabulary picker

Search hits in the subject picker show only label + URI; the vocabulary's
hierarchy is invisible until the details pane's neighborhood browse. Surface
the full broader-chain path (e.g. `LGBTQ+ people › Bi+ people`) so a
cataloger can tell "Bisexual people" from "Bisexual people in fandom" at a
glance.

## Plan

- `vocab.Index.Path(scheme, id) []TermRef`: the term's ancestor chain
  root→…→parent following skos:broader. Polyhierarchy picks the shortest
  chain (deterministic tie-break); cycles and dangling broader URIs
  terminate the walk safely.
- `GET /v1/terms?scheme=&q=` search hits gain `path` (array of TermRef);
  live suggest sources have no hierarchy, so live tabs stay as-is.
- UI: picker result rows render the path under the label (URI as fallback);
  the details pane leads with a breadcrumb (ancestors muted, term bold).
