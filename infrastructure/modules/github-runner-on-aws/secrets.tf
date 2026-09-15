# Both the secret containers and their values are managed here. Values come
# from configs/secrets.yaml (gitignored, never committed) via
# infrastructure/environments/main/terragrunt.hcl, so state ends up holding these
# credentials in plaintext — the S3 state bucket/backend must be secured
# accordingly (this is the tradeoff for a one-command apply instead of a
# manual, out-of-band `aws secretsmanager put-secret-value` step).
resource "aws_secretsmanager_secret" "github_app_private_key" {
  name = var.github_app_private_key_secret_name
  tags = var.tags
}

resource "aws_secretsmanager_secret_version" "github_app_private_key" {
  secret_id     = aws_secretsmanager_secret.github_app_private_key.id
  secret_string = var.github_app_private_key
}

resource "aws_secretsmanager_secret" "webhook_secret" {
  name = var.webhook_secret_name
  tags = var.tags
}

resource "aws_secretsmanager_secret_version" "webhook_secret" {
  secret_id     = aws_secretsmanager_secret.webhook_secret.id
  secret_string = var.webhook_secret_value
}
