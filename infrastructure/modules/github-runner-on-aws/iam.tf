# Three separate least-privilege roles — not one shared role — so a bug in
# one Lambda can't reach permissions it has no reason to hold.
data "aws_iam_policy_document" "lambda_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "basic_logging" {
  statement {
    actions   = ["logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["arn:aws:logs:*:*:*"]
  }
}

locals {
  dynamodb_gsi_arns = [
    "${aws_dynamodb_table.this.arn}/index/gsi1",
    "${aws_dynamodb_table.this.arn}/index/gsi2",
    "${aws_dynamodb_table.this.arn}/index/gsi3",
  ]
}

# ---------------------------------------------------------------------------
# webhook: verify signature, dedupe delivery, forward to SQS.
# ---------------------------------------------------------------------------
resource "aws_iam_role" "webhook" {
  name               = "${local.name_prefix}-webhook"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
  tags               = var.tags
}

data "aws_iam_policy_document" "webhook" {
  statement {
    sid       = "Logging"
    actions   = data.aws_iam_policy_document.basic_logging.statement[0].actions
    resources = data.aws_iam_policy_document.basic_logging.statement[0].resources
  }
  statement {
    sid       = "ReadWebhookSecret"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.webhook_secret.arn]
  }
  statement {
    sid       = "IdempotencyRecord"
    actions   = ["dynamodb:PutItem"]
    resources = [aws_dynamodb_table.this.arn]
  }
  statement {
    sid       = "SendToQueue"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.this.arn]
  }
}

resource "aws_iam_role_policy" "webhook" {
  name   = "${local.name_prefix}-webhook"
  role   = aws_iam_role.webhook.id
  policy = data.aws_iam_policy_document.webhook.json
}

# ---------------------------------------------------------------------------
# provision: resolve profile/group, allocate or launch a runner.
# ---------------------------------------------------------------------------
resource "aws_iam_role" "provision" {
  name               = "${local.name_prefix}-provision"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
  tags               = var.tags
}

data "aws_iam_policy_document" "provision" {
  statement {
    sid       = "Logging"
    actions   = data.aws_iam_policy_document.basic_logging.statement[0].actions
    resources = data.aws_iam_policy_document.basic_logging.statement[0].resources
  }
  statement {
    sid = "ConsumeQueue"
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:GetQueueAttributes",
    ]
    resources = [aws_sqs_queue.this.arn]
  }
  statement {
    sid       = "ReadGitHubAppPrivateKey"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.github_app_private_key.arn]
  }
  statement {
    sid = "RunnerAndJobState"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:Query",
      "dynamodb:TransactWriteItems",
    ]
    resources = concat([aws_dynamodb_table.this.arn], local.dynamodb_gsi_arns)
  }
  statement {
    sid       = "LaunchRunners"
    actions   = ["ec2:RunInstances", "ec2:CreateTags", "ec2:DescribeInstances", "ec2:DescribeImages"]
    resources = ["*"] # these do not support resource-level restriction to a launch template alone
  }
  statement {
    sid       = "ResolveLatestAMI"
    actions   = ["ssm:GetParameter"]
    resources = ["arn:aws:ssm:*::parameter/aws/service/ami-amazon-linux-latest/*"]
  }
  statement {
    sid       = "PassRunnerInstanceRole"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.instance.arn]
  }
}

resource "aws_iam_role_policy" "provision" {
  name   = "${local.name_prefix}-provision"
  role   = aws_iam_role.provision.id
  policy = data.aws_iam_policy_document.provision.json
}

# ---------------------------------------------------------------------------
# cleanup: expire idle runners, reconcile orphans, react to spot interruption.
# ---------------------------------------------------------------------------
resource "aws_iam_role" "cleanup" {
  name               = "${local.name_prefix}-cleanup"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
  tags               = var.tags
}

data "aws_iam_policy_document" "cleanup" {
  statement {
    sid       = "Logging"
    actions   = data.aws_iam_policy_document.basic_logging.statement[0].actions
    resources = data.aws_iam_policy_document.basic_logging.statement[0].resources
  }
  statement {
    sid       = "ReadGitHubAppPrivateKey"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [aws_secretsmanager_secret.github_app_private_key.arn]
  }
  statement {
    sid = "RunnerAndJobState"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:UpdateItem",
      "dynamodb:Query",
    ]
    resources = concat([aws_dynamodb_table.this.arn], local.dynamodb_gsi_arns)
  }
  statement {
    sid       = "DescribeInstances"
    actions   = ["ec2:DescribeInstances"]
    resources = ["*"] # DescribeInstances does not support resource-level restriction
  }
  statement {
    sid       = "TerminateManagedRunners"
    actions   = ["ec2:TerminateInstances"]
    resources = ["arn:aws:ec2:*:*:instance/*"]
    condition {
      test     = "StringEquals"
      variable = "ec2:ResourceTag/ManagedBy"
      values   = ["github-runner-provisioner"]
    }
  }
}

resource "aws_iam_role_policy" "cleanup" {
  name   = "${local.name_prefix}-cleanup"
  role   = aws_iam_role.cleanup.id
  policy = data.aws_iam_policy_document.cleanup.json
}
