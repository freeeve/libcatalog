# 030 -- Pluggable SSO: OIDC verifier + role middleware

## Context

Staff auth must accept any external OIDC issuer (pluggable SSO). qllpoc's
`internal/authz` (RS256 via JWKS, role claim, CanPublish) is the port source,
generalized: configurable role-claim path and role mapping instead of hardcoded
claim names. The PKCE token-exchange proxy pattern (confidential client secret
held server-side) comes along for SPA logins.

## Scope

1. `backend/auth/auth.go`: `Identity{Subject, Email, Name, Roles, Issuer}`,
   `Role` enum (`admin`, `librarian`, `moderator`, `patron`) with capability
   methods (`CanPublish`, `CanModerate`, `CanAdmin`); `TokenVerifier` interface;
   multi-verifier dispatch on the token's `iss` claim (exact match against
   configured issuers, then verify); `RequireRole(role)` middleware; identity in
   request context.
2. `backend/auth/oidc/oidc.go`: OIDC discovery + JWKS (lestrrat-go/jwx cache),
   audience/issuer/token_use checks, configurable `role_claim` + `role_map`.
3. `POST /v1/auth/exchange`: PKCE authorization_code + refresh_token grant proxy
   to the issuer's token endpoint; secret from config; 503 when unconfigured.

## Acceptance

- Table-driven verifier tests with generated keys (port qllpoc's authz tests):
  wrong issuer/aud/alg/expiry rejected, role mapping applied.
- Exchange proxy tested against an httptest issuer.
- RequireRole returns 401 without token, 403 with insufficient role.
