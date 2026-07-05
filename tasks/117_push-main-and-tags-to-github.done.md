# 117 -- Push main + recent tags to GitHub (origin is 209 commits / 3 minor versions behind)

Filed from the libcatalog-demo repo (its tasks/015, bump to v0.7.2).

`origin/main` on github.com/freeeve/libcatalog is ~209 commits behind the local
checkout, and the newest tags on GitHub are `v0.4.2` / `backend/v0.4.2` /
`hugo/v0.4.2` -- everything after (v0.5.0 .. v0.7.2, backend/v0.5.0 .. backend/v0.7.2,
and any newer hugo/ tags) exists only locally.

Consequences downstream:

- The demo repo's terraform can't pin
  `//backend/deploy/terraform/modules/readonly-demo?ref=v0.7.2` ("invalid ref"); it is
  parked on `ref=v0.4.2` with a comment (the module is byte-identical between the two
  tags today, so nothing is functionally stale -- yet).
- Anyone following the public README cannot fetch the versions the docs talk about.

Ask: `git push origin main --follow-tags` (or push the tag set explicitly) when the
local state is ready to publish. Then the demo repo can flip its terraform ref to the
real version (a no-op apply today).

## Status (2026-07-05 session)

Done. Verified origin/main was a strict fast-forward (zero divergence) and
that exactly the twelve expected annotated tags were missing remotely, then
pushed `main --follow-tags`: origin/main moved ee93ce3..52cb119 (210 commits,
tasks/106-110 + 116) and v0.5.0..v0.7.2 plus backend/v0.5.0..backend/v0.7.2
all landed as new tags. No newer hugo/ tags existed locally, so that set was
already current. The demo repo (its tasks/015) can now flip its terraform ref
from v0.4.2 to `//backend/deploy/terraform/modules/readonly-demo?ref=v0.7.2`
-- byte-identical modules today, so a no-op apply.
