include "root" {
  path = find_in_parent_folders("root.hcl")
}

locals {
  env_key       = "main"
  repo_root     = get_repo_root()
  runner_config = yamldecode(file("${local.repo_root}/configs/runner.yaml"))
  infra         = local.runner_config.infrastructure[local.env_key]
}

terraform {
  source = "${get_repo_root()}/terraform/modules/github-runner-on-aws"
}

# Provider config is generated here (not committed as a static .tf file)
# because region/tags come from configs/runner.yaml.
generate "provider" {
  path      = "provider.tf"
  if_exists = "overwrite"
  contents = <<-EOF
    provider "aws" {
      region = "${local.infra.aws_region}"

      default_tags {
        tags = ${jsonencode(merge(local.infra.tags, {
  Project     = local.infra.project_name
  Environment = local.env_key
  ManagedBy   = "terraform"
}))}
      }
    }
  EOF
}

inputs = {
  environment  = local.env_key
  project_name = local.infra.project_name

  existing_vpc_id            = local.infra.existing_vpc_id
  existing_subnet_ids        = local.infra.existing_subnet_ids
  existing_security_group_id = local.infra.existing_security_group_id

  dynamodb_point_in_time_recovery_enabled = local.infra.dynamodb_point_in_time_recovery_enabled
  enable_spot_interruption_rule           = local.infra.enable_spot_interruption_rule

  webhook_lambda_timeout   = local.infra.webhook_lambda_timeout
  provision_lambda_timeout = local.infra.provision_lambda_timeout
  cleanup_lambda_timeout   = local.infra.cleanup_lambda_timeout

  tags = local.infra.tags

  github_app_private_key_secret_name = local.runner_config.github.private_key_secret_name
  webhook_secret_name                = local.runner_config.webhook.secret_name

  # Only the app-policy sections go to the Lambdas — infrastructure is a
  # Terragrunt/Terraform-only concern the application code never needs to see.
  runner_config_json = jsonencode({
    github  = local.runner_config.github
    webhook = local.runner_config.webhook
    runner  = local.runner_config.runner
  })

  # Absolute path: Terragrunt runs the module from a cache directory, so a
  # relative path here would resolve to the wrong place.
  build_dir = "${local.repo_root}/lambda"
}
