resource "aws_cloudwatch_event_rule" "scheduled_sweep" {
  name                = "${local.name_prefix}-cleanup-sweep"
  description         = "Periodic reconciliation: expire IDLE runners, recover PROVISIONING orphans."
  schedule_expression = var.cleanup_schedule_expression
  tags                = var.tags
}

resource "aws_cloudwatch_event_target" "scheduled_sweep" {
  rule = aws_cloudwatch_event_rule.scheduled_sweep.name
  arn  = aws_lambda_function.cleanup.arn
}

resource "aws_lambda_permission" "scheduled_sweep" {
  statement_id  = "AllowEventBridgeScheduledSweep"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.cleanup.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.scheduled_sweep.arn
}

# Optional: react to an EC2 Spot interruption notice within its ~2 minute
# warning window, instead of waiting for the next scheduled sweep (which
# doesn't look at TERMINATING runners anyway). The same cleanup Lambda
# branches on detail-type (see cmd/cleanup/main.go).
resource "aws_cloudwatch_event_rule" "spot_interruption" {
  count       = var.enable_spot_interruption_rule ? 1 : 0
  name        = "${local.name_prefix}-spot-interruption"
  description = "EC2 Spot Instance Interruption Warning -> immediate runner cleanup."
  event_pattern = jsonencode({
    source      = ["aws.ec2"]
    detail-type = ["EC2 Spot Instance Interruption Warning"]
  })
  tags = var.tags
}

resource "aws_cloudwatch_event_target" "spot_interruption" {
  count = var.enable_spot_interruption_rule ? 1 : 0
  rule  = aws_cloudwatch_event_rule.spot_interruption[0].name
  arn   = aws_lambda_function.cleanup.arn
}

resource "aws_lambda_permission" "spot_interruption" {
  count         = var.enable_spot_interruption_rule ? 1 : 0
  statement_id  = "AllowEventBridgeSpotInterruption"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.cleanup.function_name
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.spot_interruption[0].arn
}
