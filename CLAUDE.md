# libcat

## Local servers -- standard testing config

Use these fixed ports and credentials; do not invent new ones.

| Instance | Port | URL | Login |
|---|---|---|---|
| Demo playground (persistent store) | 8481 | http://localhost:8481/ | eve@example.org / changeme123 |
| Throwaway verify instance (fresh store) | 8491 | http://localhost:8491/ | eve@example.org / changeme123 |

Auth flow for both: `POST /v1/auth/login` with
`{"email":"eve@example.org","password":"changeme123"}` returns
`accessToken`; send it as `Authorization: Bearer <token>`. The bootstrap
admin holds the admin role (librarian-gated routes included).

### Demo playground (port 8481)

`~/libcat-playground/run.sh` execs `~/lcatd-play` with the blob dir
`~/libcat-playground/site` -- the store is persistent, so copycat
targets, the seed marker, and edits survive restarts. Restart recipe
after each completed task (post-commit):

1. `cd backend/ui && npm run build`
2. `cd backend && go build -o ~/lcatd-play ./cmd/lcatd`
3. `cd .. && go build -o ~/libcat-playground/lcat ./cmd/lcat`
   (`cmd/lcat` is at the repo root, NOT under backend/)
4. `git checkout backend/ui/dist` (the dist in git is a placeholder;
   the real build is embedded into the binary in step 2)
5. `pkill -f lcatd-play`, then run `~/libcat-playground/run.sh` in
   the background
6. The server loads vocabularies for a few seconds before listening --
   poll `curl -s localhost:8481/` for 200 rather than assuming 2s is
   enough

### Throwaway verify instance (port 8491)

For end-to-end verification against a fresh store (e.g. seeding logic),
build and run from a scratch dir:

```sh
go build -o <scratch>/lcatd-verify ./cmd/lcatd   # from backend/
LCATD_LISTEN_ADDR=:8491 LCATD_BLOB_DIR=<scratch>/blob \
  LCATD_LOCAL_AUTH=1 \
  LCATD_BOOTSTRAP_ADMIN=eve@example.org:changeme123 \
  LCATD_ABUSE_SECRET=verify-0123456789abcdef01234567 \
  <scratch>/lcatd-verify
```

A fresh blob dir seeds the default copycat SRU targets at boot; kill the
process with `pkill -f lcatd-verify` when done.
