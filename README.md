# Provision self-hosted GitHub runners on-demand in AWS

![shield](https://img.shields.io/badge/Scope-github_runners-blue)
![shield](https://img.shields.io/badge/Cloud_provider-AWS-orange)
![shield](https://img.shields.io/badge/Terraform->=1.10-orange)
![shield](https://img.shields.io/badge/Terragrunt-1.x-blueviolet)
![shield](https://img.shields.io/badge/Language-Go-00ADD8)
![shield](https://img.shields.io/badge/Type-spot_instance-purple)

**Contents:** [Purpose](#purpose) · [Architecture](#architecture) · [Repository layout](#repository-layout) · [Setup](#setup) · [Requirements](#requirements) · [Logs](#log)

## Purpose

This project provisions self-hosted GitHub Actions runners on-demand in AWS: event-driven, idempotent, and enterprise-grade.

- **Go implementation** built around three Lambda functions (webhook, provision, cleanup) and a single DynamoDB table for state.
- **GitHub App authentication** — no personal access token, ever.
- Replaces an earlier Python/TypeScript prototype, kept untouched for reference in [src_backup/](./src_backup).

See also: [Cost estimate for expected monthly AWS spend](./docs/infrastructure/cost-estimate-2026.md).

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
infrastructure/modules/github-runner-on-aws/  The one Terraform module — every AWS resource this project needs
infrastructure/root.hcl                Shared Terragrunt config: S3 state backend (native locking, no DynamoDB)
infrastructure/environments/main/terragrunt.hcl  The single environment: reads runner.yaml, calls the module
scripts/                          build.sh, package.sh, deploy.sh
src_backup/                       Prior implementation — reference only, not part of this build
```

## Setup

**Full walkthrough: [docs/SETUP.md](./docs/SETUP.md)** — create the GitHub
App, configure `configs/runner.yaml`, deploy with Terragrunt, populate
secrets, and verify end-to-end. Condensed version:

1. Create a GitHub App (App ID, private key, webhook secret) — see [request-github-runner-token-architecture.md](./docs/infrastructure/request-github-runner-token-architecture.md). Install it on the target organization/repository.
2. Edit [configs/runner.yaml](./configs/runner.yaml) — the single source of truth for this project. Every field is documented inline (Helm `values.yaml`-style `-- comment` convention). It covers two things:
   - **App policy** (`github`/`webhook`/`runner`): App ID, secret names, scope/group, profiles and their labels.
   - **Deployment knobs** (`infrastructure.main`): region, existing-VPC IDs, Lambda timeouts, tags — read by `infrastructure/environments/main/terragrunt.hcl` and passed to the Terraform module.
3. Copy [configs/secrets.yaml](./configs/secrets.yaml) to `configs/secrets.local.yaml` and fill in the GitHub App's private key and webhook secret from step 1. This file is gitignored and never committed — Terraform reads it and applies both secrets' values directly.
4. Fill in your state bucket/region in `infrastructure/root.hcl` (this is the one thing that can't come from `configs/runner.yaml` — Terraform's backend block can't read a file that might itself be the file telling it where to find its state).
5. Build and deploy:

   ```sh
   make deploy-plan   # builds + packages Lambdas, then terragrunt plan
   make deploy-apply  # ... then terragrunt apply
   ```

   This also writes both secrets' values to Secrets Manager — no separate step needed.
6. Point the GitHub App's webhook URL at the `webhook_url` output (`cd infrastructure/environments/main && terragrunt output -raw webhook_url`).

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
