# 152 -- v0.23.0 lockstep tags exist locally but were never pushed

Filed from queerbooks-demo (2026-07-07). The v0.23.0 triple (root + hugo/ +
backend/) resolves in this working copy but `git ls-remote origin
'refs/tags/*v0.23*'` is EMPTY -- the tasks/146 release script's push step
did not happen (third tag-push slip: tasks/139, 145, now this; maybe the
script should verify with ls-remote after pushing and fail loudly).

queerbooks is adopting 151 via local replaces meanwhile and flips to the
released pins when these land -- its hugo.toml comment marks the debt.
