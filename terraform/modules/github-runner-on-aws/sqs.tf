resource "aws_sqs_queue" "dlq" {
  name                      = "${local.name_prefix}-jobs-dlq"
  message_retention_seconds = 1209600 # 14 days (max) — give time to investigate before messages expire
  tags                      = var.tags
}

resource "aws_sqs_queue" "this" {
  name = "${local.name_prefix}-jobs"

  # Must stay >= 6x the provision Lambda's timeout (AWS's own guidance):
  # otherwise a message can be redelivered to a second concurrent invocation
  # before the first's Job QUEUED->PROVISIONING conditional update (the
  # provisioning lock — see internal/store.Client.ClaimForProvisioning) has
  # even had a chance to land, defeating the lock it depends on.
  visibility_timeout_seconds = var.provision_lambda_timeout * 6

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq.arn
    maxReceiveCount     = var.sqs_max_receive_count
  })

  tags = var.tags
}
