# Three explicit functions rather than one generic module instantiated 3x —
# now that everything lives in a single module there's no cross-module
# plumbing to save by genericizing, and each function's triggers differ
# enough (HTTP API, SQS, EventBridge) that writing them directly is clearer
# than parameterizing a shared abstraction for 3 call sites.

# --- webhook -----------------------------------------------------------------

resource "aws_cloudwatch_log_group" "webhook" {
  name              = "/aws/lambda/${local.name_prefix}-webhook"
  retention_in_days = var.log_retention_in_days
  tags              = var.tags
}

resource "aws_lambda_function" "webhook" {
  function_name    = "${local.name_prefix}-webhook"
  description      = "Verifies and forwards GitHub workflow_job webhooks."
  filename         = "${var.build_dir}/webhook.zip"
  source_code_hash = filebase64sha256("${var.build_dir}/webhook.zip")
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  architectures    = ["arm64"] # must match scripts/build.sh's GOARCH
  role             = aws_iam_role.webhook.arn
  timeout          = var.webhook_lambda_timeout
  memory_size      = 256

  environment {
    variables = merge(local.common_environment, {
      QUEUE_URL = aws_sqs_queue.this.url
    })
  }

  tags       = var.tags
  depends_on = [aws_cloudwatch_log_group.webhook]
}

resource "aws_lambda_permission" "apigw" {
  statement_id  = "AllowAPIGatewayInvoke"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.webhook.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.this.execution_arn}/*/*"
}

# --- provision -----------------------------------------------------------------

resource "aws_cloudwatch_log_group" "provision" {
  name              = "/aws/lambda/${local.name_prefix}-provision"
  retention_in_days = var.log_retention_in_days
  tags              = var.tags
}

resource "aws_lambda_function" "provision" {
  function_name    = "${local.name_prefix}-provision"
  description      = "Resolves prefix/profile/group and allocates or provisions a runner."
  filename         = "${var.build_dir}/provision.zip"
  source_code_hash = filebase64sha256("${var.build_dir}/provision.zip")
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  role             = aws_iam_role.provision.arn
  timeout          = var.provision_lambda_timeout
  memory_size      = 512

  environment {
    variables = merge(local.common_environment, {
      LAUNCH_TEMPLATE_ID = aws_launch_template.runner.id
    })
  }

  tags       = var.tags
  depends_on = [aws_cloudwatch_log_group.provision]
}

resource "aws_lambda_event_source_mapping" "provision_sqs" {
  event_source_arn = aws_sqs_queue.this.arn
  function_name    = aws_lambda_function.provision.arn
  batch_size       = 5

  # Lets a batch's failures be individually retried/DLQ'd instead of the
  # entire batch — important since our idempotent-but-not-free-to-retry
  # allocation protocol means we'd rather redeliver one bad message than ten
  # good ones.
  function_response_types = ["ReportBatchItemFailures"]
}

# --- cleanup -----------------------------------------------------------------

resource "aws_cloudwatch_log_group" "cleanup" {
  name              = "/aws/lambda/${local.name_prefix}-cleanup"
  retention_in_days = var.log_retention_in_days
  tags              = var.tags
}

resource "aws_lambda_function" "cleanup" {
  function_name    = "${local.name_prefix}-cleanup"
  description      = "Expires idle runners, reconciles orphans, reacts to spot interruption."
  filename         = "${var.build_dir}/cleanup.zip"
  source_code_hash = filebase64sha256("${var.build_dir}/cleanup.zip")
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  role             = aws_iam_role.cleanup.arn
  timeout          = var.cleanup_lambda_timeout
  memory_size      = 256

  environment {
    variables = local.common_environment
  }

  tags       = var.tags
  depends_on = [aws_cloudwatch_log_group.cleanup]
}
