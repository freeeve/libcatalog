# 031 -- Built-in user management (local auth)

## Context

Deployments without an IdP need built-in users. Local auth is a second
`TokenVerifier` beside OIDC (either or both configured): user records in the
document store, argon2id password hashes, Ed25519-signed JWTs from our own
issuer, refresh tokens as store items.

## Scope

1. `backend/auth/local/local.go`: user records (`USER#<email>` PROFILE/CRED/ROLE
   items), argon2id (x/crypto/argon2, params centralized), Ed25519 token
   issuance + verification, refresh-token rotation, login rate-limit via
   `store.Increment`.
2. Routes: `POST /v1/auth/login`, `/refresh`, `/logout`; admin user management
   `GET/POST /v1/users`, `PUT /v1/users/{id}/roles`.
3. First-admin bootstrap via env (`LCATD_BOOTSTRAP_ADMIN`).
4. Multi-verifier coexistence wiring: local issuer + external OIDC in one server.

## Acceptance

- Login -> token -> RequireRole round-trip test; refresh rotation; bad-password
  rate limiting.
- Both issuers coexist in one server (token from each verifies).
- Demoable milestone: an authenticated `lcatd` with local users.
