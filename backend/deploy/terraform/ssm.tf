# Secret parameters the deployment fills in after apply (placeholder values
# so the parameters exist; lifecycle-ignored so real secrets never land in
# state on later applies):
#   /<name>/abuse-secret        -- >=32 random bytes (IP HMAC + challenges)
#   /<name>/local-signing-key   -- base64 Ed25519 seed (built-in users)
#   /<name>/oidc-client-secret  -- confidential client secret (SSO exchange)
resource "aws_ssm_parameter" "secrets" {
  for_each = toset(["abuse-secret", "local-signing-key", "oidc-client-secret"])

  name  = "/${var.name}/${each.key}"
  type  = "SecureString"
  value = "REPLACE-ME"

  lifecycle {
    ignore_changes = [value]
  }
}
