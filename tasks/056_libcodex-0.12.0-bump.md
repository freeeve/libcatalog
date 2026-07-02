# 056 -- bump libcodex to v0.12.0: 006/007 round-trip + SRU/Z39.50 additions

Filed from libcodex (its tasks 076/079/080/082, all done). Left uncommitted per
the cross-repo convention. Follows your 055 (v0.11.0 bump).

## What changed upstream

- **006/007 now round-trip** (libcodex task 082): 007 survives for the sound,
  computer and video categories via an RDA carrier correlation table (e.g. 007
  "cr" <-> carrier `cr`, "sd" <-> `sd`, "co" <-> `cd`), and a 006 leading 'm'
  (the ebook/audiobook electronic aspect) round-trips through the computer
  media type. Your `TestMARCRoundTripLossTableCurrent` will flag both on the
  bump -- move them to kept **for those shapes**: an unmapped-category 007
  (maps/globes/microforms, e.g. `ad|canzn`) stays lost, and the rebuilt 007 is
  the minimal 2-byte category+SMD, not the full position string. Positions
  beyond 00-01 and other categories are enumerated in upstream's
  `tasks/082_bibframe_006_007_coded_fields.done.md`.
- **sru**: response bodies are now capped (`Client.MaxResponseBytes`, 64 MiB
  default, distinct error when exceeded) -- relevant if you proxy user-supplied
  SRU endpoints; and a typed CQL builder (`sru.Term/And/Or/Not`) mirrors the
  z3950 builder if you want to drop hand-concatenated CQL.
- **z3950**: requesting `Syntax: "opac"` returns each bib with
  `Record.Holdings` (location, call number, circulation availability) --
  available if the copy-cataloging UI ever wants who-holds-this data.

## Acceptance

- [ ] go.mod bumped to libcodex v0.12.0.
- [ ] Loss table: 006/007 moved to kept for the mapped shapes (with the
      category caveat noted); `docs/marc-fidelity.md` updated.
- [ ] Optional: adopt `sru.Term`/CQL builder and `MaxResponseBytes` where the
      copy-cataloging client builds queries.
