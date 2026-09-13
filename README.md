# Provision self-hosted GitHub runners on-demand in AWS

![shield](https://img.shields.io/badge/Scope-github_runners-blue)
![shield](https://img.shields.io/badge/Cloud_provider-AWS-orange)
![shield](https://img.shields.io/badge/Terraform->=1.10-orange)
![shield](https://img.shields.io/badge/Terragrunt-1.x-blueviolet)
![shield](https://img.shields.io/badge/Language-Go-00ADD8)
![shield](https://img.shields.io/badge/Type-spot_instance-purple)

## Purpose

This project provisions self-hosted GitHub Actions runners on-demand in AWS: event-driven, idempotent, and enterprise-grade. It replaces an earlier Python/TypeScript prototype (kept for reference, untouched, in [src_backup/](./src_backup)) with a Go implementation built around three Lambda functions, a single DynamoDB table for state, and a GitHub App for authentication — **no personal access token, ever**.

## Architecture

See [docs/infrastructure/enterprise-standard-upgrade.md](./docs/infrastructure/enterprise-standard-upgrade.md) for the full design and [docs/infrastructure/request-github-runner-token-architecture.md](./docs/infrastructure/request-github-runner-token-architecture.md) for the GitHub App authentication flow. In short:

```text
GitHub (workflow_job webhook)
        │
        ▼
API Gateway -> webhook Lambda (verify signature, dedupe, normalize)
        │
        ▼
       SQS (+ DLQ)
        │
        ▼
provision Lambda (profile + group resolution, allocate-or-provision)
        │              │
        ▼              ▼
   DynamoDB        GitHub App (registration token) -> EC2 Spot (runner)
        ▲
        │
EventBridge (rate(3m) + spot interruption) -> cleanup Lambda
```

Developers only declare capability in their workflow — the label(s) just
need to match one of a profile's `labels` in `configs/runner.yaml`:

```yaml
runs-on: [self-hosted, erp-sport-amd64]
```

The platform — not the developer — decides AMI, instance type, Spot strategy, subnet, security group, IAM role, runner group, and registration token.

## Repository layout

```text
cmd/                              Lambda entrypoints: webhook, provision, cleanup
internal/                         Domain logic: config, github, runner, aws, store, webhook, cleanup
configs/runner.yaml               Single source of truth: app policy AND infra settings
terraform/modules/github-runner-on-aws/  The one Terraform module — every AWS resource this project needs
terraform/root.hcl                Shared Terragrunt config: S3 state backend (native locking, no DynamoDB)
terraform/environments/main/terragrunt.hcl  The single environment: reads runner.yaml, calls the module
scripts/                          build.sh, package.sh, deploy.sh
src_backup/                       Prior implementation — reference only, not part of this build
```

## Setup

**Full walkthrough: [docs/SETUP.md](./docs/SETUP.md)** — create the GitHub
App, configure `configs/runner.yaml`, deploy with Terragrunt, populate
secrets, and verify end-to-end. Condensed version:

1. Create a GitHub App (App ID, private key, webhook secret) — see [request-github-runner-token-architecture.md](./docs/infrastructure/request-github-runner-token-architecture.md). Install it on the target organization/repository.
2. Edit [configs/runner.yaml](./configs/runner.yaml) — the single source of truth for both the app policy (`github`/`webhook`/`runner`: App ID, secret names, scope/group, profiles and their labels) and the deployment knobs (`infrastructure.main`: region, existing-VPC IDs, Lambda timeouts, tags) that `terraform/environments/main/terragrunt.hcl` reads and passes to the Terraform module. Every field is documented inline (Helm `values.yaml`-style `-- comment` convention).
3. Fill in your state bucket/region in `terraform/root.hcl` (this is the one thing that can't come from `configs/runner.yaml` — Terraform's backend block can't read a file that might itself be the file telling it where to find its state).
4. Build and deploy:

   ```sh
   make deploy-plan   # builds + packages Lambdas, then terragrunt plan
   make deploy-apply  # ... then terragrunt apply
   ```

5. Populate the two Secrets Manager secrets (private key, webhook secret) out-of-band — Terraform creates the secret containers only, never their values.
6. Point the GitHub App's webhook URL at the `webhook_url` output (`cd terraform/environments/main && terragrunt output -raw webhook_url`).

## Requirements

- Go 1.26+ (see go.mod)
- Terraform >= 1.10 (native S3 state locking), AWS provider ~> 5.0
- Terragrunt >= 1.1
- AWS account with appropriate permissions
- A GitHub App installed on the target organization or repository

## Log

CloudWatch Log Groups: `/aws/lambda/<project_name>-main-webhook`, `-provision`, `-cleanup`.

## Contributing

Contributions are welcome! Please create a pull request to this repository.

## License

This project is licensed under the MIT License. See the LICENSE file for more details.
