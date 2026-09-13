locals {
  name_prefix = "${var.project_name}-${var.environment}"

  common_environment = {
    DYNAMODB_TABLE_NAME = aws_dynamodb_table.this.name
    RUNNER_CONFIG_JSON  = var.runner_config_json
  }
}
