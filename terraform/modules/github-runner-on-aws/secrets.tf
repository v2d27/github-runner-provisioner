# Creates the secret *containers* only. Values (the GitHub App's PEM private
# key, the webhook HMAC secret) are populated out-of-band, once, via the AWS
# CLI or console — consistent with this being a one-time setup
# (docs/infrastructure/request-github-runner-token-architecture.md). This
# also means `terraform apply` can never accidentally overwrite or leak a
# live credential through state.
resource "aws_secretsmanager_secret" "github_app_private_key" {
  name = var.github_app_private_key_secret_name
  tags = var.tags
}

resource "aws_secretsmanager_secret" "webhook_secret" {
  name = var.webhook_secret_name
  tags = var.tags
}
