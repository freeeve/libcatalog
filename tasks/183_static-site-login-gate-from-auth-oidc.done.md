# 183: Reusable static-site login gate built on backend/auth/oidc

Left by the queerbooks-demo session 2026-07-08 (uncommitted cross-repo ask).
Last piece of their de-Go effort (queerbooks tasks/028): Eve's rule is that
Go surviving in adopter repos must be BUILT FROM libcat code, and their
deploy/auth-lambda is currently bespoke.

The lambda (queerbooks deploy/auth-lambda, ~4 files, stdlib + aws-lambda-go
only) gates a static CloudFront-served OPAC: authorization code + PKCE
against an OIDC provider as a public client, RS256 id_token verification via
JWKS, a role-claim check (librarian/admin), then CloudFront signed cookies
(custom policy, RSA-SHA1) so every other path on the distribution is gated;
unauthenticated hits bounce through the 403 custom error page to /_auth/gate.

You already own the OIDC half (backend/auth/oidc: discovery, PKCE exchange,
JWKS verification). Ask: an importable gate package (say auth/sitegate) that
composes that with the CloudFront signed-cookie minting + the /_auth/*
routing, so an adopter's lambda main is a ~20-line wrapper: load config,
lambda.Start(sitegate.Handler(cfg)). The aws-lambda-go dependency can stay
on the adopter side of the boundary (handler takes/returns plain HTTP-shaped
structs) if you want the core dependency-free per ARCHITECTURE. queerbooks'
lambda is a working reference incl. tests (deploy/auth-lambda).

## Outcome

Shipped `backend/auth/sitegate` (947234c, released v0.37.0): an
importable, net/http-only login gate composing auth/oidc with CloudFront
signed-cookie minting.

- `sitegate.New(ctx, Config)` -> `*Gate` (http.Handler) serving
  {PathPrefix}/login, /callback, /logout, /gate (default `/_auth`).
  Required config: Issuer, ClientID, SiteDomain, KeyPairID,
  PrivateKeyPEM (PKCS#1 or PKCS#8). Optional: MinRole (default
  librarian, ranked so admin passes), RoleClaim/RoleMap/JWKSURL
  passthrough to oidc, Scopes, SiteName, SessionTTL (default 12h).
- id_token verification rides oidc.Verifier (JWKS cache, iss/aud/exp,
  string-or-array aud handled by jwx), replacing the lambda's bespoke
  RS256 code; auth/oidc now exports Discover/Discovery with
  authorization_endpoint.
- aws-lambda-go stays adopter-side as asked: Function URLs share the
  API Gateway v2 payload shape, so the existing backend/awslambda
  adapter wraps the gate. Whole adopter main is in the package's
  Example (~15 lines): `lambda.Start(awslambda.Handler(gate))`.
- Hardening kept/added: open-redirect guard also rejects `/\` forms
  (fuzzed); role-gated 403 sets no CloudFront cookies; fail-closed on
  unsignable key. Coverage 86%.
- Filed the adoption ask back to queerbooks-demo (rebuild
  deploy/auth-lambda on sitegate; cookie name changes to
  sitegate_flow, forbidden message is generic).
