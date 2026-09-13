# Single-table design backing internal/store. See that Go package's doc
# comment for the full pk/sk/GSI schema this table implements.
resource "aws_dynamodb_table" "this" {
  name         = "${local.name_prefix}-runners"
  billing_mode = "PAY_PER_REQUEST" # spiky, webhook-driven traffic — not worth capacity planning
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
  attribute {
    name = "gsi1pk"
    type = "S"
  }
  attribute {
    name = "gsi1sk"
    type = "S"
  }
  attribute {
    name = "gsi2pk"
    type = "S"
  }
  attribute {
    name = "gsi2sk"
    type = "N"
  }
  attribute {
    name = "gsi3pk"
    type = "S"
  }
  attribute {
    name = "gsi3sk"
    type = "S"
  }

  # Allocation pool: find an IDLE runner for {scope, owner, profile, group}.
  global_secondary_index {
    name            = "gsi1"
    hash_key        = "gsi1pk"
    range_key       = "gsi1sk"
    projection_type = "ALL"
  }

  # Status timeline: cleanup's expiry sweep + stuck-in-PROVISIONING sweep,
  # for both Runner and Job items.
  global_secondary_index {
    name            = "gsi2"
    hash_key        = "gsi2pk"
    range_key       = "gsi2sk"
    projection_type = "ALL"
  }

  # Instance reverse lookup: orphan reconciliation + spot-interruption
  # handling.
  global_secondary_index {
    name            = "gsi3"
    hash_key        = "gsi3pk"
    range_key       = "gsi3sk"
    projection_type = "ALL"
  }

  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = var.dynamodb_point_in_time_recovery_enabled
  }

  server_side_encryption {
    enabled = true
  }

  tags = var.tags
}
