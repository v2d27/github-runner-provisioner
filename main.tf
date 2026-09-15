# Root passthrough module — lets a consumer reference this repo's root
# directly as a module source (git::.../github-runner-provisioner.git or a
# local clone under their own modules/ dir) instead of having to know about
# the internal infrastructure/modules/github-runner-on-aws layout. All actual
# resources live in that nested module; this file only forwards inputs/outputs.
module "github_runner_on_aws" {
  source = "./infrastructure/modules/github-runner-on-aws"

  environment  = var.environment
  project_name = var.project_name

  runner_config_json                 = var.runner_config_json
  github_app_private_key_secret_name = var.github_app_private_key_secret_name
  webhook_secret_name                = var.webhook_secret_name
  github_app_private_key             = var.github_app_private_key
  webhook_secret_value               = var.webhook_secret_value

  build_dir = var.build_dir

  existing_vpc_id            = var.existing_vpc_id
  existing_subnet_ids        = var.existing_subnet_ids
  existing_security_group_id = var.existing_security_group_id
  vpc_cidr_block             = var.vpc_cidr_block
  subnet_cidr_blocks         = var.subnet_cidr_blocks
  allow_egress_cidr_blocks   = var.allow_egress_cidr_blocks

  dynamodb_point_in_time_recovery_enabled = var.dynamodb_point_in_time_recovery_enabled
  dynamodb_ttl_days                       = var.dynamodb_ttl_days
  webhook_lambda_timeout                  = var.webhook_lambda_timeout
  provision_lambda_timeout                = var.provision_lambda_timeout
  cleanup_lambda_timeout                  = var.cleanup_lambda_timeout
  ready_lambda_timeout                    = var.ready_lambda_timeout
  sqs_max_receive_count                   = var.sqs_max_receive_count
  cleanup_schedule_expression             = var.cleanup_schedule_expression
  enable_spot_interruption_rule           = var.enable_spot_interruption_rule
  log_retention_in_days                   = var.log_retention_in_days

  tags = var.tags
}
