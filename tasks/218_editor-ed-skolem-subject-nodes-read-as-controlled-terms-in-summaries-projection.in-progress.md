# 218 -- editor -ed- skolem subject nodes read as controlled terms in summaries/projection

Opened 2026-07-09. Found while building tasks/217 (clone).

The convention splitting controlled from uncontrolled subjects is the
object's term kind: `bf:subject` with an IRI object reads as a
controlled term (ingest.SummarizeDataset -> WorkSummary.Subjects,
project.subjectsAndTags -> Work.Subjects), a blank node with rdfs:label
reads as an uncontrolled heading (-> Tags).

But the editor's chained-field write shape violates it: an `add` on the
subjectLabels field (predicates [bf:subject, rdfs:label]) renders
`<work> bf:subject <#<work>Work-ed-subjectLabels>` -- an IRI object --
so the hand-added uncontrolled heading surfaces as a controlled subject
IRI (`#...-ed-subjectLabels`) in the admin summary, the subject facet,
and the public projection, instead of as a tag.

tasks/217 sidestepped it for clones (uncontrolled headings stay with
the source), but the leak exists on any work a cataloger adds a
subjectLabel to. Options: teach the readers to treat grain-local
fragment IRIs (`#...`) as uncontrolled; or render subjectLabels adds as
lcat:tag instead; or resolve the label through the skolem node in the
readers. Decide with the subjects model, not ad hoc.

## Outcome

Fixed in v0.68.0 (commit 862e0a3) with option 1, stated as a model
rule: `bibframe.GrainLocalIRI` -- a fragment node the grain itself
minted is never a controlled-term reference. `ingest.SummarizeDataset`
and the projector's `subjectsAndTags` now surface a labeled grain-local
bf:subject node as a tag (like the blank node it stands in for); the
subject facet and public catalog follow from the summaries. Regression
tests in both readers.

Severity correction to the filing above: the editor cannot actually
emit the shape today -- every default-profile field under bf:subject /
bf:genreForm (subjectLabels, genreForm) is read-only, and the add is
refused (verified live: 400 "field subjectLabels is read-only"). The
exposure was a custom profile making those fields writable, or a raw
editorial PUT. The fix stands as defense-in-depth so the readers
enforce the convention no matter what profile a deployment ships.
