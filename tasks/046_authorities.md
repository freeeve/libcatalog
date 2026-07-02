# 046 -- Authorities: local authority editing, merge, linking

## Context

Koha's authorities module, SKOS-native: local authority grains at
`data/authorities/` (ARCHITECTURE §3), ids minted `a...`, edited with the same
profile mechanism; cross-references are altLabel (used-for) and broader/
narrower/related (BT/NT/RT). Global heading update is free -- bibs reference by
URI, so relabels propagate at projection; merges rewrite references.

## Scope

1. `backend/authoritiesvc/`: authority CRUD over authority grains (blob.Store),
   label search, `identity.Mint("a")` for local terms; authority profiles
   (authority-topic shipped default).
2. Authority merge: `lcat:mergedInto` on the loser + batch rewrite of
   `bf:subject` references loser -> winner across work grains (one batch op).
3. Auto-linking suggestions (never auto-write): on Work save, string subject
   values matched against authority labels -> suggestion queue items; unmatched
   heading -> one-keystroke "create local authority" flow.
4. SPA: Authorities search screen + AuthorityEditor (prefLabel/altLabel/
   definition/broader/narrower/related/exactMatch), merge tool.

## Acceptance

- Create/edit/merge round-trips through grains; merged references rewritten
  corpus-wide; vocab index reflects changes after reload.
- Auto-link suggestions land in the queue with provenance PIPELINE.
