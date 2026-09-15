---
paths:
  - "infrastructure/modules/github-runner-on-aws/**"
---

# Root Terraform files are a hand-kept mirror of this module

`main.tf`, `variables.tf`, `outputs.tf`, and `versions.tf` at the repo root
are a thin passthrough module that wraps
`infrastructure/modules/github-runner-on-aws` so external projects can
consume this repo as a Terraform module from its root
(`source = "git::.../github-runner-provisioner.git"` or a local
`./modules/github-runner-provisioner` vendor path) without knowing about the
internal module path. See the README's "Use as a Terraform module" section.

**Whenever you change this module's `variables.tf`, `outputs.tf`, or
`versions.tf`, update the matching root file to match:**

- Add/remove/rename a variable here → make the same change in root
  `variables.tf`, and add/remove/rename the corresponding passthrough line
  (`var.foo = var.foo`) in root `main.tf`'s `module "github_runner_on_aws"`
  block.
- Add/remove/rename an output here → make the same change in root
  `outputs.tf`, sourcing the value from `module.github_runner_on_aws.<name>`.
- Change `required_version` or `required_providers` here → mirror it in root
  `versions.tf`.

The two files are meant to have identical variable/output surfaces — the root
ones just proxy through. After editing, run `terraform fmt` and
`terraform validate` at the repo root to confirm (`terraform init -backend=false`
at the root is enough — no AWS credentials or state backend needed).

Do **not** duplicate resource logic at the root — the root files must only
declare variables/outputs and the single passthrough `module` block. All
actual AWS resources live in this module's own `*.tf` files.

Note: `infrastructure/environments/main/terragrunt.hcl` intentionally points
straight at this nested module (a stable path that predates the root
passthrough), not at the repo root — don't "fix" that to point at the root.
