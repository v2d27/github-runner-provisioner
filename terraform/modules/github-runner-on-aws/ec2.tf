# Shared, profile-agnostic runner infra: network (or a reference to an
# existing one), security group, IAM instance profile, and a launch template
# that fixes the static shape. AMI, instance type and UserData are
# deliberately left unset here — the provision Lambda supplies them per call
# from configs/runner.yaml (internal/aws.AMIResolver + internal/runner),
# so adding a new runner profile never requires a terraform apply.

data "aws_availability_zones" "available" {
  count = var.existing_vpc_id == null ? 1 : 0
  state = "available"
}

resource "aws_vpc" "this" {
  count                = var.existing_vpc_id == null ? 1 : 0
  cidr_block           = var.vpc_cidr_block
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = merge(var.tags, { Name = "${local.name_prefix}-vpc" })
}

resource "aws_internet_gateway" "this" {
  count  = var.existing_vpc_id == null ? 1 : 0
  vpc_id = aws_vpc.this[0].id
  tags   = merge(var.tags, { Name = "${local.name_prefix}-igw" })
}

resource "aws_route_table" "public" {
  count  = var.existing_vpc_id == null ? 1 : 0
  vpc_id = aws_vpc.this[0].id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this[0].id
  }

  tags = merge(var.tags, { Name = "${local.name_prefix}-public-rt" })
}

resource "aws_subnet" "public" {
  count                   = var.existing_vpc_id == null ? length(var.subnet_cidr_blocks) : 0
  vpc_id                  = aws_vpc.this[0].id
  cidr_block              = var.subnet_cidr_blocks[count.index]
  availability_zone       = data.aws_availability_zones.available[0].names[count.index]
  map_public_ip_on_launch = true
  tags                    = merge(var.tags, { Name = "${local.name_prefix}-public-${count.index}" })
}

resource "aws_route_table_association" "public" {
  count          = length(aws_subnet.public)
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public[0].id
}

resource "aws_security_group" "runner" {
  count       = var.existing_security_group_id == null ? 1 : 0
  name_prefix = "${local.name_prefix}-runner-"
  description = "GitHub Actions self-hosted runner instances — outbound only."
  vpc_id      = local.vpc_id

  egress {
    description = "Runner needs outbound HTTPS to GitHub and package repositories"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = var.allow_egress_cidr_blocks
  }

  tags = var.tags

  lifecycle {
    create_before_destroy = true
  }
}

locals {
  vpc_id            = var.existing_vpc_id != null ? var.existing_vpc_id : aws_vpc.this[0].id
  subnet_ids        = var.existing_subnet_ids != null ? var.existing_subnet_ids : aws_subnet.public[*].id
  security_group_id = var.existing_security_group_id != null ? var.existing_security_group_id : aws_security_group.runner[0].id
}

data "aws_iam_policy_document" "instance_assume_role" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "instance" {
  name               = "${local.name_prefix}-runner-instance"
  assume_role_policy = data.aws_iam_policy_document.instance_assume_role.json
  tags               = var.tags
}

resource "aws_iam_instance_profile" "runner" {
  name = "${local.name_prefix}-runner"
  role = aws_iam_role.instance.name
}

resource "aws_launch_template" "runner" {
  name_prefix = "${local.name_prefix}-runner-"

  iam_instance_profile {
    arn = aws_iam_instance_profile.runner.arn
  }

  # Single-AZ in this version — runners all launch into subnet_ids[0].
  # Multi-AZ spreading would need the provision Lambda to pick a subnet per
  # RunInstances call; left as a follow-up.
  network_interfaces {
    associate_public_ip_address = true
    security_groups             = [local.security_group_id]
    subnet_id                   = local.subnet_ids[0]
  }

  metadata_options {
    http_tokens   = "required" # IMDSv2 only
    http_endpoint = "enabled"
  }

  monitoring {
    enabled = true
  }

  tag_specifications {
    resource_type = "instance"
    tags          = var.tags
  }

  lifecycle {
    create_before_destroy = true
  }
}
