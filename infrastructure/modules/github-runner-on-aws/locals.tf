locals {
  name_prefix = "${var.project_name}-${var.environment}"

  common_environment = {
    DYNAMODB_TABLE_NAME = aws_dynamodb_table.this.name
    RUNNER_CONFIG_JSON  = var.runner_config_json
    DYNAMODB_TTL_DAYS   = tostring(var.dynamodb_ttl_days)
  }

  # Shared with the provision Lambda (READY_CALLBACK_URL, baked into every
  # instance's UserData) and the ready Lambda's own route.
  ready_callback_url = "${aws_apigatewayv2_stage.default.invoke_url}runner-ready"
}
