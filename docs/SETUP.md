# Setup guide

A complete, one-time walkthrough for standing up this platform from a clean
checkout: create the GitHub App, edit the single config file, deploy with
Terragrunt, wire up secrets, and verify a real workflow picks up a runner.

Follow the phases in order — each one has a checkpoint so you know it
actually worked before moving to the next.

## Prerequisites

Install these before starting:

| Tool | Version | Check |
|---|---|---|
| Go | 1.26+ | `go version` |
| Terraform | >= 1.10 | `terraform version` |
| Terragrunt | >= 1.1 | `terragrunt --version` |
| AWS CLI | v2 | `aws --version` |
| `zip` | any | `zip --version` |

You'll also need:

- An AWS account, with credentials configured locally (`aws sts get-caller-identity` should print your account).
- Admin access to the GitHub organization or repository you're deploying runners for.

---

## Phase 1 — Create the Terraform state bucket

Terraform's backend configuration is the one thing that can't live in
`configs/runner.yaml` — the backend block can't read a file that might
itself be the file telling it where to find its state. This is a one-time,
per-AWS-account setup.

```sh
BUCKET=your-company-terraform-state   # must be globally unique
REGION=ap-southeast-1

aws s3api create-bucket \
  --bucket "$BUCKET" \
  --region "$REGION" \
  --create-bucket-configuration LocationConstraint="$REGION"

aws s3api put-bucket-versioning \
  --bucket "$BUCKET" \
  --versioning-configuration Status=Enabled
```

Then edit **`terraform/root.hcl`** and replace the two placeholder locals:

```hcl
locals {
  state_bucket = "your-company-terraform-state"  # <- the bucket you just created
  state_region = "ap-southeast-1"                # <- must match
}
```

State locking uses S3's own conditional-write locking (`use_lockfile`) —
there's no separate DynamoDB lock table to create.

> Alternative: if you'd rather let Terragrunt create the bucket for you,
> skip the `aws s3api` commands above and add `--backend-bootstrap` to the
> `terragrunt init` command in Phase 5.

**Checkpoint:** `aws s3 ls "s3://$BUCKET"` returns without error.

---

## Phase 2 — Create the GitHub App

This platform authenticates as a GitHub App — never a personal access
token (see [request-github-runner-token-architecture.md](./infrastructure/request-github-runner-token-architecture.md)).

1. Go to **github.com/settings/apps/new** (personal account) or your
   organization's **Settings → Developer settings → GitHub Apps → New
   GitHub App** (recommended if `runner.scope: organization`).
2. Fill in:
   - **GitHub App name**: anything, e.g. `erp-sport-runner-provisioner`.
   - **Homepage URL**: this repo's URL is fine.
   - **Webhook → Active**: checked.
   - **Webhook URL**: leave a placeholder for now, e.g. `https://example.com/webhook` — you'll come back and fix this in Phase 7, after Terraform gives you the real one.
   - **Webhook secret**: generate one now and save it somewhere safe —
     you'll need it again in Phase 6:
     ```sh
     openssl rand -hex 32
     ```
3. **Permissions** — set based on `runner.scope` in `configs/runner.yaml`:
   - `scope: repository` → **Repository permissions → Administration: Read and write**
   - `scope: organization` → **Organization permissions → Self-hosted runners: Read and write**
4. **Subscribe to events**: check **Workflow jobs** (and only that — this
   platform ignores every other event type).
5. **Where can this GitHub App be installed**: "Only on this account" is fine.
6. Click **Create GitHub App**.
7. Note the **App ID** shown at the top of the app's settings page.
8. Scroll to **Private keys → Generate a private key**. A `.pem` file
   downloads — keep it, you'll upload its contents in Phase 6.
9. Click **Install App** (left sidebar) and install it on the
   organization/repository matching your `runner.yaml` scope.

**Checkpoint:** you have an App ID (a number), a downloaded `.pem` file, and
a webhook secret string, and the app shows as installed.

---

## Phase 3 — Configure `configs/runner.yaml`

This is the single source of truth for both the app's runtime policy and
the Terraform deployment. Every field has an inline `-- description`
comment (Helm `values.yaml` style) — open the file alongside this guide.

Required edits:

```yaml
github:
  app_id: <the App ID from Phase 2, step 7>
  # installation_id: leave as 0 — resolved automatically on first use

runner:
  scope: repository            # or "organization" — must match Phase 2, step 3
  organization: "my-org"       # your GitHub org
  repository: "erp-sport"      # required if scope: repository
  group: ""                    # must stay "" when scope: repository

  profiles:
    amd64:
      labels: [erp-sport-amd64]   # <- name these yourself; see note below
      # ships with ami_lookup (amazon-linux/2023) — resolves automatically,
      # no edit needed unless you want a different OS/pinned AMI.
      ...
    arm64:
      labels: [erp-sport-arm64]
      ami: "ami-yyyyyyyyyyyyyyyyy"   # <- REQUIRED: replace with a real AMI ID,
                                       #    or switch to ami_lookup like amd64
```

There's no separate platform-wide prefix — a workflow's `runs-on:` matches
a profile if it includes **any one** of that profile's `labels`. If you
want to avoid colliding with an unrelated workflow in the same GitHub
organization, name your labels yourself using `<project_name>-<profile>`,
e.g. `erp-sport-amd64` — that's what the shipped example does. Leaving
`labels` empty defaults it to the profile's own key (e.g. `amd64`), which
is fine if you don't need that namespacing.

Then, in `infrastructure.main`:

```yaml
infrastructure:
  main:
    aws_region: ap-southeast-1     # <- your region
    project_name: provision-github-runner
    existing_vpc_id: null           # leave null: creates its own minimal VPC
    existing_subnet_ids: null
    existing_security_group_id: null
```

Leave the VPC fields as `null` for a first deployment — it creates its own
self-contained VPC. Fill in `existing_vpc_id` / `existing_subnet_ids` if you
want to deploy into an existing, already-governed VPC instead.

**Checkpoint:** no more `CHANGEME`/`ami-xxxx…`/`ami-yyyy…` placeholders left
in the file.

---

## Phase 4 — Build the Lambda binaries

```sh
make package
```

This cross-compiles all three Lambdas (`scripts/build.sh`) and zips them
(`scripts/package.sh`) into `lambda/{webhook,provision,cleanup}.zip`.
Terraform reads these zips directly — always run this before deploying.

**Checkpoint:** `ls lambda/*.zip` shows three files.

---

## Phase 5 — Deploy

```sh
make deploy-plan
```

Review the plan — you should see roughly 40 resources to add (DynamoDB
table, SQS queue + DLQ, VPC + subnet + security group, IAM roles, 3 Lambda
functions, API Gateway, EventBridge rules) and nothing to destroy on a first
run. Then:

```sh
make deploy-apply
```

Confirm when prompted. This takes a few minutes.

> If you skipped creating the S3 bucket in Phase 1, add
> `--backend-bootstrap` to the underlying `terragrunt init` — either export
> `TF_CLI_ARGS_init="--backend-bootstrap"` before running `make
> deploy-apply`, or `cd terraform/environments/main && terragrunt init
> --backend-bootstrap` once manually first.

**Checkpoint:**

```sh
cd terraform/environments/main
terragrunt output -raw webhook_url
```

prints a URL like `https://abc123.execute-api.ap-southeast-1.amazonaws.com/webhook`.

---

## Phase 6 — Populate the secrets

Terraform created the two Secrets Manager secret *containers* but
deliberately never touches their *values* — that's a manual, one-time step:

```sh
aws secretsmanager put-secret-value \
  --secret-id github-runner/app/private-key \
  --secret-string file:///path/to/your-downloaded-key.pem

aws secretsmanager put-secret-value \
  --secret-id github-runner/webhook/secret \
  --secret-string "the-webhook-secret-you-generated-in-phase-2"
```

(Use whatever secret names you actually put in
`configs/runner.yaml`'s `github.private_key_secret_name` /
`webhook.secret_name` if you changed the defaults.)

**Checkpoint:**

```sh
aws secretsmanager get-secret-value --secret-id github-runner/app/private-key --query VersionId --output text
```

returns a version ID (not an error).

---

## Phase 7 — Point the GitHub App at your webhook

Back in the GitHub App's settings (**Settings → Developer settings → GitHub
Apps → your app → General**):

1. Set **Webhook URL** to the `webhook_url` output from Phase 5's checkpoint.
2. Confirm **Webhook secret** matches exactly what you put in Secrets
   Manager in Phase 6 (re-paste it if you're not sure).
3. Save changes.

**Checkpoint:** GitHub's App settings page has a **Recent Deliveries** tab
(under **Advanced**) — it'll show delivery attempts once a workflow runs
(next phase).

---

## Phase 8 — Verify end-to-end

Trigger a workflow in the repository the app is installed on, with a job
whose `runs-on:` includes one of a profile's labels:

```yaml
jobs:
  test:
    runs-on: [self-hosted, erp-sport-amd64]   # <- one of your profile's labels
    steps:
      - run: echo "hello from a provisioned runner"
```

Watch the Lambdas react:

```sh
aws logs tail /aws/lambda/provision-github-runner-main-webhook   --since 5m --follow
aws logs tail /aws/lambda/provision-github-runner-main-provision --since 5m --follow
```

(open a second terminal for the second command). You should see: the
webhook Lambda log a forwarded `queued` event, then the provision Lambda
log `provisioned new runner`. Within a couple of minutes the job should
pick up the new self-hosted runner in GitHub's Actions UI.

**Checkpoint:** the workflow run completes on a runner named
`amd64-<something>` (visible in the job's log header, or under the
repo/org's **Settings → Actions → Runners** while it's briefly online).

If nothing happens, see **Troubleshooting** below.

---

## Troubleshooting

**Webhook returns 401 / GitHub shows a failed delivery.** The webhook
secret in GitHub doesn't match Secrets Manager. Re-check Phase 6/7 — the
webhook Lambda logs `invalid webhook signature` when this happens
(`aws logs tail /aws/lambda/<project_name>-main-webhook`).

**Workflow stays queued, no runner ever appears.** Check the provision
Lambda's logs for `ignoring job with no matching profile` — none of your
profiles' `labels` appear in the job's `runs-on:` list.

**`RunInstances`/`AuthFailure` in the provision Lambda logs.** Usually a bad
AMI ID for the region you deployed to (the placeholder `ami-yyyy…` in the
`arm64` profile is not real — see Phase 3) or an IAM permission gap; check
the error message, it names the missing action.

**`terragrunt init` fails with a bucket/access error.** The state bucket in
`terraform/root.hcl` doesn't exist or your AWS credentials can't reach it —
revisit Phase 1.

**A runner never gets cleaned up / EC2 keeps running.** Check
`/aws/lambda/<project_name>-main-cleanup` logs — the scheduled sweep runs
every 3 minutes; give it a few cycles before assuming it's broken.

---

## Day-2 operations

- **Change a profile (instance type, AMI, Spot on/off, labels) or add a new
  one**: edit `configs/runner.yaml`, then `make deploy-apply` — no code
  changes needed, and existing runners aren't affected (only new ones use
  the updated profile).
- **Change idle timeout or group**: same — edit the YAML, redeploy.
- **Tear down**:
  ```sh
  cd terraform/environments/main
  terragrunt destroy
  ```
  (Secrets Manager secrets aren't destroyed automatically if they still
  hold a value and deletion protection kicks in — confirm the prompt.)
