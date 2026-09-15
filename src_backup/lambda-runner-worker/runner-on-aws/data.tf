data "aws_iam_role" "this" {
  name = "GithubRunnerEC2InstanceRole"
}

data "aws_iam_policy" "this" {
  name = "GithubRunnerEC2DescribeTerminatePolicy"
}

data "aws_iam_instance_profile" "this" {
  name = "GithubRunnerInstanceProfile" 
}

data "aws_caller_identity" "current" {}

data "aws_subnet" "this" {
    id = "subnet-5002f518"
}