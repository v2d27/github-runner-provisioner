# Contributing

Thanks for looking at improving `github-runner-provisioner`. This is a small
project, so the process is intentionally light — read this, run the checks
below before you push, and open a PR.

Start with [README.md](./README.md) for the architecture overview and
[docs/SETUP.md](./docs/SETUP.md) if you need a working deployment to test
against. [docs/infrastructure/enterprise-standard-upgrade.md](./docs/infrastructure/enterprise-standard-upgrade.md)
has the full design rationale.

## Development setup

- Go 1.26+ (`go version`, see `go.mod`)
- Terraform >= 1.10 and Terragrunt >= 1.1 (`.terraform-version` /
  `.terragrunt-version` pin the exact versions CI uses)
- AWS CLI v2 and `zip`, only if you're actually deploying

## Project layout

See the [Repository layout](./README.md#repository-layout) section of the
README. In short:

- `cmd/<name>/main.go` — the four Lambda entrypoints (webhook, provision,
  cleanup, ready). Keep these thin; they wire AWS/Lambda plumbing to a
  handler in `internal/<name>`.
- `internal/` — all actual logic (config, github, runner, aws, store,
  webhook, cleanup, ready).
- `configs/runner.yaml` / `configs/secrets.yaml` — checked-in templates.
  Never edit or commit `configs/*.local.yaml` (gitignored, holds real
  values).
- `infrastructure/modules/github-runner-on-aws/` — the single Terraform
  module; there's deliberately only one environment (`environments/main`).

## Making changes

### Go code

```sh
make fmt    # gofmt -l . — must print nothing
make vet    # go vet ./...
make test   # go test ./...
make build  # cross-compiles all four cmd/* Lambdas
```

Run these before pushing — CI (`.github/workflows/build.yml`) enforces the
same three checks plus a build. There's no test suite yet; if you're adding
non-trivial logic (especially in `internal/runner`, `internal/store`, or
`internal/github`), add table-driven tests alongside it rather than leaving
it uncovered.

### Config schema changes

`configs/runner.yaml` is documented inline using a Helm `values.yaml`-style
`# -- (type) description` convention above each field. If you add, rename,
or change the meaning of a field, update its comment in the same commit,
and check whether `docs/SETUP.md` or `README.md` reference it.

### Terraform / Terragrunt

```sh
tfswitch
tgswitch

make tf-fmt       # terragrunt hcl fmt --check, terraform fmt -check
make tf-validate  # credential-free: validates HCL/types, no AWS account or state bucket needed
```

### Docs

If a change affects setup steps, config fields, the Lambda topology, or the
architecture, update `README.md` and/or `docs/SETUP.md` in the same PR —
don't let them drift from the code.

## Commits and PRs

- Branch off `main` as `feature/<short-description>` (matches this repo's
  existing history) and open a PR back into `main`.
- Keep PRs scoped to one change; explain *why*, not just *what*, in the
  description.
- Note what you actually verified (e.g. `make fmt vet test tf-fmt
  tf-validate`, or a real `deploy-plan`/`deploy-apply` against a test AWS
  account) — full end-to-end verification needs AWS credentials that CI
  doesn't have by default.

## Secrets

Never commit `configs/runner.local.yaml`, `configs/secrets.local.yaml`, a
GitHub App private key, or a webhook secret. Both are gitignored already —
double-check `git status`/`git diff` before pushing if you've been editing
config locally.

## License

By contributing, you agree your contributions are licensed under this
project's [Apache License 2.0](./LICENSE).
