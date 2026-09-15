terraform {
  required_version = ">= 1.10" # S3 native state locking (use_lockfile) — see infrastructure/root.hcl

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.64.0"
    }
  }
}
