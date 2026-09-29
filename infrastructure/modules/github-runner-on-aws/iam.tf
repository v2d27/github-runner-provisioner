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
# ready: confirms a runner slot actually registered with GitHub and started
# (the runner-ready callback instances call back via UserData).
# ---------------------------------------------------------------------------
resource "aws_iam_role" "ready" {
  name               = "${local.name_prefix}-ready"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
  tags               = var.tags
}

data "aws_iam_policy_document" "ready" {
  statement {
    sid       = "Logging"
    actions   = data.aws_iam_policy_document.basic_logging.statement[0].actions
    resources = data.aws_iam_policy_document.basic_logging.statement[0].resources
  }
  statement {
    sid       = "ConfirmRunnerSlot"
    actions   = ["dynamodb:GetItem", "dynamodb:UpdateItem"]
    resources = [aws_dynamodb_table.this.arn]
  }
}

resource "aws_iam_role_policy" "ready" {
  name   = "${local.name_prefix}-ready"
  role   = aws_iam_role.ready.id
  policy = data.aws_iam_policy_document.ready.json
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
      # ListLiveInstances (internal/store/runner.go) scans the base table
      # directly rather than a GSI — see that function's own doc comment for
      # why a Scan is the deliberate choice here. Without this action, every
      # scheduled sweep fails outright before it can expire a single idle
      # runner or catch a stuck boot.
      "dynamodb:Scan",
    ]
    resources = concat([aws_dynamodb_table.this.arn], local.dynamodb_gsi_arns)
  }
  statement {
    sid       = "DescribeInstances"
    actions   = ["ec2:DescribeInstances"]
    resources = ["*"] # DescribeInstances does not support resource-level restriction
  }
  # Runs `ps aux | grep -E 'Runner.Listener|Runner.Worker'` on runner
  # instances (internal/aws/ssm.go) to confirm no job is executing before
  # terminating one. Limited to the stock AWS-RunShellScript document and to
  # instances tagged as this platform's runners.
  statement {
    sid       = "RunnerProcessCheckDocument"
    actions   = ["ssm:SendCommand"]
    resources = ["arn:aws:ssm:*::document/AWS-RunShellScript"]
  }
  statement {
    sid       = "RunnerProcessCheckInstances"
    actions   = ["ssm:SendCommand"]
    resources = ["arn:aws:ec2:*:*:instance/*"]
    condition {
      test     = "StringEquals"
      variable = "ssm:resourceTag/ManagedBy"
      values   = ["github-runner-provisioner"]
    }
  }
  statement {
    sid       = "RunnerProcessCheckResults"
    actions   = ["ssm:ListCommandInvocations", "ssm:DescribeInstanceInformation"]
    resources = ["*"] # neither supports resource-level restriction
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

# Just enough for the runner instances' SSM agent to register and receive Run
# Command (the cleanup Lambda's runner process check). Deliberately not the
# AmazonSSMManagedInstanceCore managed policy: that also grants
# ssm:GetParameter(s) on every parameter in the account, and every CI job on
# a runner can use the instance's credentials.
data "aws_iam_policy_document" "instance_ssm_agent" {
  statement {
    sid = "SSMAgentRunCommand"
    actions = [
      "ssm:UpdateInstanceInformation",
      "ec2messages:AcknowledgeMessage",
      "ec2messages:DeleteMessage",
      "ec2messages:FailMessage",
      "ec2messages:GetEndpoint",
      "ec2messages:GetMessages",
      "ec2messages:SendReply",
      "ssmmessages:CreateControlChannel",
      "ssmmessages:CreateDataChannel",
      "ssmmessages:OpenControlChannel",
      "ssmmessages:OpenDataChannel",
    ]
    resources = ["*"] # none of these support resource-level restriction
  }
}

resource "aws_iam_role_policy" "instance_ssm_agent" {
  name   = "${local.name_prefix}-runner-ssm-agent"
  role   = aws_iam_role.instance.id
  policy = data.aws_iam_policy_document.instance_ssm_agent.json
}
