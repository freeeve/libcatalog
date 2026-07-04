# 086: Default search target + BISAC headings

Two fresh-deployment paper cuts from the tasks/083-084 review:

- "Look up subjects at targets…" errored with "no search targets
  configured" until an admin visited the copycat screen. Done: the copycat
  service seeds the LOC SRU target (open, anonymous, Bath-profile indexes)
  on a store that has never had targets -- once ever, marker-guarded, so
  deleting every target sticks across restarts.
- Classification rendered as the raw BISAC code ("DRA000000"). Done: the
  ~55 BISAC section names ship as UI data (lib/bisac.ts); a general code
  reads as its heading ("Drama / General", code in the tooltip), a
  specific subheading falls back to its section name with the code as a
  muted chip (the full ~5,000-entry list is BISG-licensed, so it is not
  vendored). Renders like subjects: heading + BISACSH scheme chip, id
  demoted.
