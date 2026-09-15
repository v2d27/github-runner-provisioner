# Root Terragrunt config, included by every terraform/environments/<env>/terragrunt.hcl.
# Owns the one thing that can't live in configs/runner.yaml: the state
# backend itself (Terraform's `backend` block cannot use variables or
# locals, so it can't read a file — this is the one hardcoded exception).
#
# Uses Terraform's native S3 state locking (`use_lockfile`, Terraform >=
# 1.10) instead of a separate DynamoDB lock table — one less resource to
# provision and pay for just to run `terraform apply`.
locals {
  state_bucket = "github-runner-terraform-state"
  state_region = "ap-southeast-1"
}

remote_state {
  backend = "s3"
  generate = {
    path      = "backend.tf"
    if_exists = "overwrite"
  }
  config = {
    bucket       = local.state_bucket
    key          = "github-runner-provisioner/${path_relative_to_include()}/terraform.tfstate"
    region       = local.state_region
    encrypt      = true
    use_lockfile = true
  }
}
