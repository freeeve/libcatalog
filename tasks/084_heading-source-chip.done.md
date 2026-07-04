# 084: Heading-source chip in the native view

Follow-up to tasks/083: vendor subject headings rendered without their MARC
$2 attribution ("Drama." said feed:marc but not OverDrive), because the
heading's bf:source label lives one hop deeper than the claimed leaf.

Done: profiles gain an optional `annotation` predicate chain -- resolved
from each value's structure node into a display-only qualifier riding on
the FieldValue (`annotation`); the quads stay in passthrough, so the
byte-identical doc round trip is untouched. Chained fields only (the value
needs a structure node), validated against the known vocabularies.
subjectLabels and classification annotate with bf:source -> rdfs:label, so
headings read "Drama. · OVERDRIVE" and classification "DRA000000 ·
BISACSH" in the profile form (same muted scheme-chip styling as the
controlled-subject chips).

Candidate follow-up: the identifiers field could annotate the same way
(024 $2 "OverDrive, Inc."), stacked with its existing kind badge.
