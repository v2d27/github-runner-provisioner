resource "aws_iam_role" "this" {
  count = length(data.aws_iam_role.this.id) == 0 ? 1 : 0
  name               = "GithubRunnerEC2InstanceRole"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect = "Allow"
        Action = "sts:AssumeRole"
        Principal = {
          Service = "ec2.amazonaws.com"
        }
      }
    ]
  })
}

resource "aws_iam_policy" "this" {
  count = length(data.aws_iam_policy.this.id) == 0 ? 1 : 0
  name        = "GithubRunnerEC2DescribeTerminatePolicy"
  description = "Policy to allow EC2 DescribeInstances and TerminateInstances actions"
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Effect    = "Allow"
        Action    = [
          "ec2:DescribeInstances",
          "ec2:TerminateInstances"
        ]
        Resource  = "*"
      }
    ]
  })
}

resource "aws_iam_role_policy_attachment" "this" {
  count = length(data.aws_iam_policy.this.id) == 0 ? 1 : 0
  role       = aws_iam_role.this[0].name
  policy_arn = aws_iam_policy.this[0].arn
}

# Create IAM Instance Profile
resource "aws_iam_instance_profile" "this" {
  count = length(data.aws_iam_instance_profile.this.id) == 0 ? 1 : 0
  name = "GithubRunnerInstanceProfile"
  role = aws_iam_role.this[0].name
}