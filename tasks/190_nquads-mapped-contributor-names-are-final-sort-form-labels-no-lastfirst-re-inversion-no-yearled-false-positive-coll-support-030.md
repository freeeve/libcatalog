# 190 -- nquads mapped contributor names are final sort-form labels: no lastFirst re-inversion, no yearLed false positive (coll-support 030)

Filed from coll-support on 2026-07-08 (cross-repo ask).

Root cause of queerbooks' remaining 5-grain parity residue (their tasks/034
audit at v0.41.0; coll-support tasks/030). The coll-feed contract's
dcterms:contributor literals are FINAL label forms: the exporter already
lastFirsts transcribed person names and passes OverDrive creator sortNames
verbatim (which is also how corporate/direct-form names ride). Two provider
behaviors break that:

1. **ingest/nquads/record.go:156 re-applies lastFirst(name) to the mapped
   contributor name.** Comma-bearing person names pass through (no-op), but
   comma-less direct forms invert wrongly: "Barefoot Books" -> "Books,
   Barefoot", "Twin Cities GLBT Oral History Project" -> "Project, Twin
   Cities GLBT Oral History", "Christo Casas" -> "Casas, Christo" (grains
   wsiciscndbibeq, wrl2u3t3mrppba, wtemuhlsu6cucq, w1dkou17c25uj0; the old
   qbd pipeline carried the OD sortNames verbatim). Fix: use the mapped
   name as the Label unchanged. (The 069 creator-FALLBACK lastFirst is
   fine -- creators are raw access points, not sort forms.)

2. **The 186 junk gate's yearLed test runs on the sort-form name.**
   "5000, Alaska Thunderfuck (narrator)" -- a real narrator credit (drag
   artist Alaska 5000, OD sortName form) -- matches ^\d{4}\b and drops,
   and the creator fallback then fabricates role "author" (grain for
   coll:6994). A comma-bearing "NNNN, Rest" is an inverted name, not a
   bare copyright-year line; exempt comma-bearing names from yearLed (the
   heuristic targets raw debris like "1999" / "2011 EMI Records Ltd.").
   qbd ran the junk gate on the RAW pre-inversion name, so it never hit
   this.

Repro: ingest coll-support's catalog.coll.nq (22:28 export or later --
its literals are verifiably direct-form: grep
'<urn:coll:work:85424> .*contributor' gives "Barefoot Books (author)")
and diff the five grains against queerbooks works-qbd-pre030flip; all
five should converge, closing their parity residue to zero.
