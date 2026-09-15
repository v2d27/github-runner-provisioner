output "webhook_url" {
  description = "Register this + \"/webhook\" as the GitHub App's webhook URL."
  value       = module.github_runner_on_aws.webhook_url
}

output "ready_callback_url" {
  description = "The runner-ready callback URL baked into every instance's UserData — for reference/debugging only, not something you configure by hand."
  value       = module.github_runner_on_aws.ready_callback_url
}

output "dynamodb_table_name" {
  value = module.github_runner_on_aws.dynamodb_table_name
}

output "sqs_queue_url" {
  value = module.github_runner_on_aws.sqs_queue_url
}

output "sqs_dlq_url" {
  value = module.github_runner_on_aws.sqs_dlq_url
}
