# CLAUDE.md

High-level orientation for Claude Code in this repo. For detail, follow the
links below rather than duplicating them here.

## Purpose

Provisions self-hosted GitHub Actions runners on-demand in AWS: event-driven,
idempotent, GitHub App–authenticated (no PAT). Go Lambdas + one DynamoDB
table for state. Full picture: [README.md](./README.md).

## Architecture (one line)

`GitHub webhook -> API Gateway -> webhook Lambda -> SQS -> provision Lambda
-> EC2 (Spot/On-Demand) -> ready Lambda callback`, with EventBridge driving
periodic + spot-interruption `cleanup`. Full diagram and rationale:
[README.md#architecture](./README.md#architecture),
[docs/infrastructure/enterprise-standard-upgrade.md](./docs/infrastructure/enterprise-standard-upgrade.md).

## Repository structure

- `cmd/<name>/` — thin Lambda entrypoints (webhook, provision, cleanup, ready)
- `internal/` — the actual logic, one package per concern (config, github, runner, aws, store, webhook, cleanup, ready)
- `configs/*.yaml` — checked-in templates; real values go in gitignored `configs/*.local.yaml`, never committed
- `main.tf` / `variables.tf` / `outputs.tf` / `versions.tf` (repo root) — passthrough module wrapping the module below, so this repo can be consumed as a Terraform module from its root
- `infrastructure/modules/github-runner-on-aws/` — the one real Terraform module (all AWS resources)
- `infrastructure/environments/main/` + `infrastructure/root.hcl` — this project's own Terragrunt deployment of that module
- `docs/` — architecture, setup walkthrough, cost estimate
- `src_backup/` — prior implementation, reference only, not part of the build

See [README.md#repository-layout](./README.md#repository-layout) for the
authoritative version of this list.

## Workflow

- Go: `make fmt vet test build` before pushing
- Terraform/Terragrunt: `make tf-fmt tf-validate` (credential-free)
- Branch off `main` as `feature/<short-description>`, PR back into `main`; keep PRs scoped to one change
- If a change touches setup steps, config fields, or architecture, update `README.md`/`docs/SETUP.md` in the same PR
- Full conventions: [CONTRIBUTING.md](./CONTRIBUTING.md)

## Scoped rules

Path-specific instructions live under [.claude/rules/](./.claude/rules/) and
load automatically when Claude touches matching files — check there before
editing `infrastructure/modules/github-runner-on-aws/`.
