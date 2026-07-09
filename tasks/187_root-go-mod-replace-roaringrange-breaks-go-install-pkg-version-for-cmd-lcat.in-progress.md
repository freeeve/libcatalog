# 187 -- root go.mod replace (roaringrange) breaks go install pkg@version for cmd/lcat

Opened 2026-07-08.

Found while fixing tasks/185 (same disease, root module): go.mod ships
`replace github.com/freeeve/roaringrange => ../roaringrange`, so
`go install github.com/freeeve/libcat/cmd/lcat@vX` fails the same way
backend's commands did. Unlike 185's in-repo replace, this one wires a
SIBLING repo checkout, so the fix is a workflow decision: the local
go.work (untracked, from 185) can carry the replace as a `replace`
line for co-dev sessions, but dropping it from go.mod means in-repo
builds without the sibling checkout resolve roaringrange v0.30.0 from
the proxy -- decide whether any current work depends on unreleased
roaringrange before flipping. Adopters go-installing lcat (queerbooks'
deploy builds it from source today) are the beneficiaries.
