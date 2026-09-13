terraform {
  required_version = ">= 1.10" # S3 native state locking (use_lockfile) — see terraform/terragrunt.hcl

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}
