locals {
  project_name = "autorunnerv2"
}

# ===================================================================================
# Create a Lambda function
# ===================================================================================
resource "aws_lambda_function" "pyautorunner" {
  function_name = local.project_name
  description = "Calculate creating new github runner api v2"
  handler       = "main.lambda_handler"
  runtime       = "python3.12"
  role          = aws_iam_role.this.arn
  filename      = data.archive_file.lambda_zip.output_path
  source_code_hash = data.archive_file.lambda_zip.output_base64sha256
  publish = true

  environment {
    variables = {
      GITHUB_TOKEN = var.github-token
      GITHUB_ORG = var.github-org
    }
  }

  depends_on = [ 
    aws_cloudwatch_log_group.this 
  ]
}

resource "aws_lambda_permission" "api_gateway" {
  statement_id  = "AllowExecutionFromAPIGateway"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.pyautorunner.function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.this.execution_arn}/*/*"
}

resource "aws_cloudwatch_log_group" "this" {
  name              = "/aws/lambda/${local.project_name}"
  retention_in_days = 3
}

# ===================================================================================
# Create API Gateway
# ===================================================================================
resource "aws_apigatewayv2_api" "this" {
  name          = "${local.project_name}api"
  protocol_type = "HTTP"
}

resource "aws_apigatewayv2_stage" "runner" {
  api_id = aws_apigatewayv2_api.this.id
  name        = "runner"
  auto_deploy = true
}
resource "aws_apigatewayv2_integration" "lambda_integration" {
  api_id             = aws_apigatewayv2_api.this.id
  integration_type   = "AWS_PROXY"
  integration_uri    = aws_lambda_function.pyautorunner.invoke_arn
  integration_method = "POST"
}

resource "aws_apigatewayv2_route" "lambda_route" {
  api_id    = aws_apigatewayv2_api.this.id
  route_key = "POST /request"
  target    = "integrations/${aws_apigatewayv2_integration.lambda_integration.id}"
}