output "webhook_url" {
  description = "Register this + \"/webhook\" as the GitHub App's webhook URL."
  value       = "${aws_apigatewayv2_stage.default.invoke_url}/webhook"
}

output "dynamodb_table_name" {
  value = aws_dynamodb_table.this.name
}

output "sqs_queue_url" {
  value = aws_sqs_queue.this.url
}

output "sqs_dlq_url" {
  value = aws_sqs_queue.dlq.url
}
