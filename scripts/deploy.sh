#!/usr/bin/env bash
# Thin terragrunt wrapper for the single "main" environment: init, then plan
# or apply.
#
# Usage:
#   scripts/deploy.sh plan
#   scripts/deploy.sh apply
#
# Always run scripts/build.sh and scripts/package.sh first — this script
# does not build anything itself. All deployment configuration (region, VPC,
# timeouts, tags, ...) comes from configs/runner.yaml's
# infrastructure.main block, read directly by terragrunt.hcl — there is no
# tfvars file to fill in.
set -euo pipefail

ACTION="${1:?usage: deploy.sh <plan|apply|destroy>}"

cd "$(dirname "$0")/../infrastructure/environments/main"

terragrunt init -input=false
terragrunt "${ACTION}"
