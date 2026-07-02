# The sidecar's single document-store table (backend/store/dynamo): pk/sk,
# TTL on expireAt, PITR for the audit trail. No GSIs by design -- services
# maintain their own index items.
resource "aws_dynamodb_table" "sidecar" {
  name         = "${var.name}-sidecar"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "S"
  }

  ttl {
    attribute_name = "expireAt"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = true
  }
}
