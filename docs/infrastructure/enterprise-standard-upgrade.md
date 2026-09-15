# enterprise-standard-upgrade

Below is the **complete architecture + implementation workflow** for `github-runner-provisioner`, integrating **2 new requirements**:

1. **Runner labels in the workflow must contain a configurable prefix**.
2. **The platform can configure the GitHub Runner Group**; `empty` = GitHub Default Runner Group.
3. this repository is onetime setup, end user can configure via yaml file before applying to AWS infrastructure
4. Use golang for all lambda function
5. EC2 can be provisioned to the existing infrastructure or deploy new infrastructure
6. the final structure: Lamda function controller will receive the event from github via github apps, never call to github to proceed the ec2. Only allow to request GitHub to get github runner token via github app.

The architecture goal is **enterprise-grade, event-driven, idempotent, scalable**, while still keeping the **runner pool + 5-minute idle reuse** model.

---

## 1. Target Architecture

```text
                         ┌──────────────────────────────┐
                         │       Developer               │
                         │                              │
                         │ .github/workflows/*.yml      │
                         │                              │
                         │ runs-on:                     │
                         │   - self-hosted              │
                         │   - erp-sport                │
                         │   - linux                    │
                         │   - amd64                    │
                         └──────────────┬───────────────┘
                                        │
                                        │ workflow_job
                                        ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                                GitHub                                        │
│                                                                              │
│  Workflow Engine                                                             │
│       │                                                                      │
│       ├── queued                                                              │
│       ├── in_progress                                                         │
│       └── completed                                                           │
│                │                                                             │
│                │ Webhook                                                      │
│                ▼                                                             │
│        ┌───────────────────┐                                                 │
│        │    GitHub App     │                                                 │
│        └─────────┬─────────┘                                                 │
└──────────────────┼───────────────────────────────────────────────────────────┘
                   │
                   │ HTTPS
                   ▼
        ┌──────────────────────┐
        │     API Gateway      │
        │   Webhook endpoint   │
        └──────────┬───────────┘
                   │
                   ▼
        ┌──────────────────────┐
        │   Webhook Lambda     │
        │                      │
        │ - Verify signature   │
        │ - Parse event        │
        │ - Idempotency        │
        │ - Normalize event    │
        └──────────┬───────────┘
                   │
                   │ normalized event
                   ▼
        ┌──────────────────────┐
        │         SQS          │
        │   Runner Job Queue   │
        │                      │
        │ + DLQ                │
        └──────────┬───────────┘
                   │
                   ▼
        ┌──────────────────────────────────────┐
        │       Runner Controller Lambda        │
        │                                      │
        │  1. Validate prefix                  │
        │  2. Resolve runner profile           │
        │  3. Resolve runner group             │
        │  4. Reconcile capacity               │
        │  5. Allocate existing runner         │
        │  6. Provision new runner if needed   │
        │  7. Update DynamoDB                  │
        └──────────────┬───────────────┬───────┘
                       │               │
             ┌─────────┘               └───────────────┐
             ▼                                         ▼
┌────────────────────────────┐             ┌─────────────────────────────┐
│         DynamoDB           │             │        GitHub API            │
│                            │             │                              │
│ Runner state               │             │ Installation Token           │
│ Job state                  │             │ Registration Token            │
│ Workflow state             │             │ Runner registration           │
│ Idempotency                │             │ Runner status                 │
│ Distributed locks          │             │ Runner removal                │
│ Configuration              │             │ Runner groups                 │
└─────────────┬──────────────┘             └─────────────────────────────┘
              │
              ▼
┌──────────────────────────────────────────────────────────────┐
│                         AWS EC2 Spot/OnDemand                │
│                                                              │
│  ┌────────────────────┐   ┌────────────────────┐             │
│  │ x86_64 Runner      │   │ ARM64 Runner      │             │
│  │                    │   │                    │             │
│  │ GitHub Runner      │   │ GitHub Runner      │             │
│  │ persistent temp    │   │ persistent temp    │             │
│  └────────────────────┘   └────────────────────┘             │
└──────────────────────────────────────────────────────────────┘


                 ┌────────────────────────────┐
                 │       EventBridge           │
                 │       every 3 minute        │
                 └──────────────┬─────────────┘
                                ▼
                 ┌────────────────────────────┐
                 │      Cleanup Lambda        │
                 │                            │
                 │ IDLE + expired             │
                 │      ↓                     │
                 │ TERMINATING                │
                 │      ↓                     │
                 │ Remove runner              │
                 │      ↓                     │
                 │ Terminate EC2              │
                 └────────────────────────────┘
```

---

## 2. Core Design Principle

The workflow **does not call the runner provisioning API**.

The developer only declares capability:

```yaml
jobs:
  build:
    runs-on:
      - self-hosted
      - erp-sport
      - linux
      - amd64
```

The platform will decide:

```text
erp-sport + linux + amd64
        │
        ▼
Runner Profile
        │
        ├── AMI = x86_64
        ├── Instance = c7i.large
        ├── Spot = true
        ├── Runner Group = aws-spot-runners
        └── Lifecycle = 5-minute idle timeout
```

The developer **must not know or decide**:

```text
AWS instance type
AMI
Spot strategy
subnet
security group
runner group
IAM role
registration token
```

This is the separation between:

```text
Developer intent
        ↓
Platform policy
        ↓
AWS infrastructure
```

---

## 3. Requirement #1 — Runner Prefix

This is a very important requirement to prevent the controller from mistakenly picking up jobs from another runner platform.

### Configuration

Example:

```yaml
runner:
  prefix: "erp-sport"
```

Workflow:

```yaml
runs-on:
  - self-hosted
  - erp-sport
  - linux
  - amd64
```

The controller receives:

```json
{
  "labels": [
    "self-hosted",
    "erp-sport",
    "linux",
    "amd64"
  ]
}
```

Validation:

```text
self-hosted       ✓
erp-sport         ✓
linux             ✓
amd64             ✓
```

If:

```yaml
runs-on:
  - self-hosted
  - linux
  - amd64
```

then:

```text
prefix = erp-sport
        ↓
not found
        ↓
IGNORE
        ↓
DO NOT PROVISION
```

#### The prefix should not be used as a partial match

Should not do:

```text
strings.Contains(label, "erp-sport")
```

Because:

```text
erp-sport
erp-sport-dev
my-erp-sport
```

will cause ambiguity.

Should use exact match:

```go
func hasRequiredPrefix(labels []string, prefix string) bool {
    for _, label := range labels {
        if label == prefix {
            return true
        }
    }

    return false
}
```

---

## 4. Requirement #2 — Runner Group

Configuration:

```yaml
runner:
  prefix: "erp-sport"

  group: ""
```

`group: ""` means:

```text
GitHub Default Runner Group
```

If:

```yaml
runner:
  prefix: "erp-sport"
  group: "aws-spot-runners"
```

then the runner must be registered to:

```text
aws-spot-runners
```

Example:

```text
GitHub Organization
│
├── Default
│   ├── GitHub-hosted
│   └── ...
│
└── aws-spot-runners
    ├── erp-sport-i-001
    ├── erp-sport-i-002
    └── erp-sport-i-003
```

**The runner group should not be placed inside `runs-on`.**

Do not do:

```yaml
runs-on:
  - self-hosted
  - erp-sport
  - aws-spot-runners
  - amd64
```

The developer only requests:

```yaml
runs-on:
  - self-hosted
  - erp-sport
  - linux
  - amd64
```

The platform decides the runner group.

---

## 5. Configuration Model

I recommend the following configuration:

```yaml
runner:
  prefix: "erp-sport"

  group: ""

  idle_timeout: 5m

  profiles:
    amd64:
      labels:
        - linux
        - amd64

      architecture: x86_64
      instance_type: c7i.large
      ami: ami-xxxxxxxx

    arm64:
      labels:
        - linux
        - arm64

      architecture: arm64
      instance_type: c7g.large
      ami: ami-yyyyyyyy
```

Later, this can be extended to:

```yaml
profiles:
  amd64-small:
    labels:
      - linux
      - amd64
      - small

  amd64-large:
    labels:
      - linux
      - amd64
      - large

  arm64:
    labels:
      - linux
      - arm64
```

Workflow:

```yaml
runs-on:
  - self-hosted
  - erp-sport
  - linux
  - amd64
  - large
```

Controller mapping:

```text
erp-sport
linux
amd64
large
        ↓
amd64-large
        ↓
c7i.xlarge
```

---

## 6. Runner Lifecycle

The runner does not use `--ephemeral`.

Because the current requirement is **reuse within 5 minutes**.

Lifecycle:

```text
                  ┌─────────────┐
                  │ PROVISIONING│
                  └──────┬──────┘
                         │
                         ▼
                  ┌─────────────┐
                  │   STARTING  │
                  └──────┬──────┘
                         │
                         ▼
                  ┌─────────────┐
                  │    IDLE     │◄──────────────┐
                  └──────┬──────┘               │
                         │                      │
                    job queued                  │
                         │                      │
                         ▼                      │
                  ┌─────────────┐               │
                  │    BUSY     │               │
                  └──────┬──────┘               │
                         │                      │
                    job completed               │
                         │                      │
                         ▼                      │
                  ┌─────────────┐               │
                  │    IDLE     │───────────────┘
                  └──────┬──────┘
                         │
                  5 min expired
                         │
                         ▼
                  ┌─────────────┐
                  │ TERMINATING  │
                  └──────┬──────┘
                         │
                         ▼
                  ┌─────────────┐
                  │ TERMINATED  │
                  └─────────────┘
```

---

## 7. Workflow Example

### Sequential jobs

```yaml
jobs:
  build:
    runs-on:
      - self-hosted
      - erp-sport
      - linux
      - amd64

  test:
    needs: build
    runs-on:
      - self-hosted
      - erp-sport
      - linux
      - amd64

  deploy:
    needs: test
    runs-on:
      - self-hosted
      - erp-sport
      - linux
      - amd64
```

Timeline:

```text
build queued
    ↓
Provision EC2 #1
    ↓
build running
    ↓
build completed
    ↓
runner #1 IDLE
terminate_after = now + 5m
    ↓
test queued
    ↓
reuse runner #1
    ↓
test completed
    ↓
runner #1 IDLE
terminate_after = now + 5m
    ↓
deploy queued
    ↓
reuse runner #1
```

=> **Only 1 EC2 is needed.**

---

## 8. Parallel Workflow

```yaml
jobs:
  build-api:
    runs-on:
      - self-hosted
      - erp-sport
      - linux
      - amd64

  build-web:
    runs-on:
      - self-hosted
      - erp-sport
      - linux
      - amd64

  build-arm:
    runs-on:
      - self-hosted
      - erp-sport
      - linux
      - arm64
```

The controller sees:

```text
amd64 = 2 jobs
arm64 = 1 job
```

Capacity:

```text
amd64:
  current = 0
  pending = 0
  desired = 2

arm64:
  current = 0
  pending = 0
  desired = 1
```

Provision:

```text
2 × amd64 EC2
1 × arm64 EC2
```

---

## 9. Controller Reconciliation

This is the most important part.

The controller does not simply:

```text
job queued → create EC2
```

But must:

```text
GitHub event
     ↓
Desired state
     ↓
Current state
     ↓
Reconcile
```

Formula:

```text
required capacity
    -
available capacity
    -
pending capacity
    =
new capacity required
```

Example:

```text
desired = 3
idle = 1
busy = 1
provisioning = 0

available = 2

create = 3 - 2 = 1
```

---

## 10. Atomic Allocation

Two Lambdas may run concurrently:

```text
Lambda A ─────┐
              ├──→ same queued job
Lambda B ─────┘
```

Both must not be allowed to provision a runner.

DynamoDB conditional update:

```text
IDLE
 ↓
BUSY
```

only one Lambda is allowed to succeed.

Concept:

```go
UpdateItem(
    ConditionExpression: "attribute_exists(pk) AND #status = :idle",
    UpdateExpression: `
        SET #status = :busy,
            busy_at = :now
        REMOVE terminate_after
    `,
)
```

The other Lambda:

```text
ConditionalCheckFailed
        ↓
runner already allocated
        ↓
reconcile again
```

---

## 11. DynamoDB Model

I recommend clearly separating entities.

### Runner

```json
{
  "pk": "RUNNER#runner-01JABC",
  "sk": "STATE",

  "github_runner_id": 123456,

  "scope": "repository",
  "organization": "my-org",
  "repository": "erp-sport",

  "prefix": "erp-sport",
  "group": "aws-spot-runners",

  "profile": "amd64",

  "labels": [
    "self-hosted",
    "erp-sport",
    "linux",
    "amd64"
  ],

  "architecture": "x86_64",
  "instance_id": "i-0123456789",
  "instance_type": "c7i.large",

  "status": "IDLE",

  "job_id": null,
  "workflow_run_id": null,

  "created_at": "...",
  "idle_since": "...",
  "terminate_after": "..."
}
```

---

## 12. Job State

```json
{
  "pk": "JOB#987654",
  "sk": "STATE",

  "job_id": 987654,
  "workflow_run_id": 123456,

  "repository": "erp-sport",

  "labels": [
    "self-hosted",
    "erp-sport",
    "linux",
    "amd64"
  ],

  "profile": "amd64",

  "status": "QUEUED",

  "runner_id": null,

  "created_at": "...",
  "updated_at": "..."
}
```

---

## 13. Idempotency

GitHub webhooks can retry.

Therefore:

```text
delivery_id
```

must be stored.

Example:

```text
pk = EVENT#github-delivery-id
```

If the event already exists:

```text
duplicate
   ↓
ACK
   ↓
do nothing
```

Do not provision again.

---

## 14. Webhook Flow

```text
GitHub
  │
  │ workflow_job
  ▼
API Gateway
  │
  ▼
Webhook Lambda
  │
  ├── Verify X-Hub-Signature-256
  │
  ├── Verify GitHub App webhook secret
  │
  ├── Extract delivery ID
  │
  ├── Idempotency check
  │
  ├── Parse workflow_job
  │
  └── Push normalized event
        │
        ▼
       SQS
```

Normalized event:

```json
{
  "event_id": "github-delivery-id",
  "event": "workflow_job",
  "action": "queued",

  "job_id": 987654,
  "workflow_run_id": 123456,

  "organization": "my-org",
  "repository": "erp-sport",

  "labels": [
    "self-hosted",
    "erp-sport",
    "linux",
    "amd64"
  ]
}
```

---

## 15. Prefix Validation

Controller:

```text
workflow_job
      ↓
labels
      ↓
contains exact configured prefix?
      │
      ├── NO → ignore
      │
      └── YES
             ↓
       resolve profile
```

Example:

```text
configured prefix:
erp-sport

incoming:
self-hosted
linux
amd64

result:
IGNORE
```

Incoming:

```text
self-hosted
erp-sport
linux
amd64

result:
PROCESS
```

---

## 16. Profile Resolution

Should not hard-code:

```go
if arch == "amd64" {
    instanceType = "c7i.large"
}
```

Split into a resolver:

```go
type RunnerProfile struct {
    Name          string
    Labels        []string
    Architecture  string
    AMI           string
    InstanceType  string
}
```

Resolver:

```go
profile, err := profileResolver.Resolve(labels)
```

Example:

```text
[self-hosted, erp-sport, linux, amd64]
                 ↓
             amd64
                 ↓
       c7i.large / x86_64
```

---

## 17. Runner Registration

EC2 UserData:

```text
1. Install GitHub Actions Runner
2. Receive registration token
3. Configure runner
4. Set runner name
5. Set labels
6. Set runner group
7. Start runner service
```

Concept:

```bash
./config.sh \
  --url https://github.com/my-org \
  --token "$RUNNER_TOKEN" \
  --name "$RUNNER_NAME" \
  --labels "self-hosted,erp-sport,linux,amd64" \
  --runnergroup "$RUNNER_GROUP" \
  --unattended
```

If:

```text
group = ""
```

then **do not pass a runner group override**, so GitHub uses the Default group.

---

## 18. GitHub App Authentication

Do not use a PAT.

Flow:

```text
GitHub App Private Key
        ↓
App JWT
        ↓
Installation Access Token
        ↓
GitHub API
        ↓
Runner Registration Token
        ↓
EC2 UserData
```

Secrets:

```text
AWS Secrets Manager
└── github/app/private-key
```

The Lambda only needs permission:

```text
secretsmanager:GetSecretValue
```

---

## 19. Provisioning Flow

```text
workflow_job: queued
        │
        ▼
Controller
        │
        ├── prefix validation
        │
        ├── profile resolution
        │
        ├── find IDLE runner
        │
        ├──── found ────────────────┐
        │                           │
        │                           ▼
        │                    IDLE → BUSY
        │
        └──── not found
                    │
                    ▼
              capacity check
                    │
                    ▼
              acquire lock
                    │
                    ▼
              GitHub registration token
                    │
                    ▼
              EC2 Spot RunInstances
                    │
                    ▼
              UserData
                    │
                    ▼
              GitHub runner register
                    │
                    ▼
              STARTING
                    │
                    ▼
              IDLE / BUSY
```

---

## 20. Cleanup Flow

EventBridge:

```text
rate(3 minute)
```

→ Cleanup Lambda.

Query:

```text
status = IDLE
AND terminate_after <= now
```

Then:

```text
IDLE
 ↓
conditional update
 ↓
TERMINATING
 ↓
remove GitHub runner
 ↓
TerminateInstances
 ↓
TERMINATED
```

Important: must re-check before terminating.

Example:

```text
22:00 runner IDLE
terminate_after = 22:05

22:04:59
new job arrives
runner → BUSY
```

Cleanup at:

```text
22:05
```

must see:

```text
status != IDLE
```

→ do not terminate.

---

## 21. GitHub Runner Group Validation

When configured:

```yaml
group: "aws-spot-runners"
```

the controller should validate that the group exists before provisioning.

```text
config loaded
     ↓
GitHub API
     ↓
runner group exists?
     │
     ├── YES → continue
     │
     └── NO → fail configuration
```

Should not allow:

```text
EC2 created
   ↓
runner registration failed
   ↓
orphan EC2
```

The group ID/name can be cached in DynamoDB/config cache.

---

## 22. Repository / Organization Scope

Config should support:

```yaml
runner:
  prefix: "erp-sport"
  group: ""
  scope: repository
```

or:

```yaml
runner:
  scope: organization
```

Repository example:

```text
github.com/company/erp-sport
```

runner registration:

```text
https://github.com/company/erp-sport
```

Organization:

```text
https://github.com/company
```

The controller must keep the scope in DynamoDB to avoid mistakenly allocating runners across repositories.

---

## 23. Recommended Repository Structure

```text
github-runner-provisioner/
│
├── cmd/
│   ├── webhook/
│   │   └── main.go
│   │
│   ├── controller/
│   │   └── main.go
│   │
│   └── cleanup/
│       └── main.go
│
├── internal/
│   │
│   ├── controller/
│   │   ├── reconcile.go
│   │   ├── scheduler.go
│   │   ├── capacity.go
│   │   └── allocation.go
│   │
│   ├── runner/
│   │   ├── profile.go
│   │   ├── resolver.go
│   │   ├── lifecycle.go
│   │   └── userdata.go
│   │
│   ├── github/
│   │   ├── app.go
│   │   ├── installation.go
│   │   ├── runners.go
│   │   ├── runner_groups.go
│   │   └── webhook.go
│   │
│   ├── aws/
│   │   ├── ec2.go
│   │   ├── spot.go
│   │   └── ami.go
│   │
│   ├── store/
│   │   ├── dynamodb.go
│   │   ├── runners.go
│   │   ├── jobs.go
│   │   └── idempotency.go
│   │
│   └── config/
│       └── config.go
│
├── config/
│   └── runners.yaml
│
├── infrastructure/
│   │
│   ├── modules/
│   │   ├── api-gateway/
│   │   ├── lambda/
│   │   ├── sqs/
│   │   ├── dynamodb/
│   │   ├── ec2/
│   │   ├── iam/
│   │   ├── secrets-manager/
│   │   └── eventbridge/
│   │
│   └── environments/
│       ├── dev/
│       └── prod/
│
├── docs/
│   ├── architecture.md
│   ├── runner-lifecycle.md
│   ├── controller.md
│   ├── configuration.md
│   └── operations.md
│
└── .github/
    └── workflows/
```

---

## 24. Implementation Workflow

I recommend implementing in **10 phases**, not all at once.

### Phase 1 — Domain model

Implement:

```text
Runner
RunnerStatus
RunnerProfile
Job
WorkflowJobEvent
RunnerConfig
```

States:

```go
const (
    RunnerProvisioning = "PROVISIONING"
    RunnerStarting     = "STARTING"
    RunnerIdle         = "IDLE"
    RunnerBusy         = "BUSY"
    RunnerTerminating  = "TERMINATING"
    RunnerTerminated   = "TERMINATED"
)
```

---

### Phase 2 — Configuration

Implement:

```yaml
runner:
  prefix: "erp-sport"
  group: ""
  idle_timeout: 5m
```

Plus profiles.

Validation:

```text
prefix required
idle_timeout > 0
profile labels unique
AMI architecture matches
instance type valid
```

---

### Phase 3 — GitHub App

Implement:

```text
App JWT
Installation Token
Registration Token
Remove Runner
List Runners
Runner Groups
```

Interfaces:

```go
type GitHubClient interface {
    GetInstallationToken(...)
    GetRegistrationToken(...)
    RegisterRunner(...)
    RemoveRunner(...)
    ListRunners(...)
    ListRunnerGroups(...)
}
```

---

## 25. Phase 4 — Webhook

Implement:

```text
API Gateway
   ↓
Webhook Lambda
   ↓
signature verification
   ↓
idempotency
   ↓
SQS
```

Events:

```text
workflow_job.queued
workflow_job.in_progress
workflow_job.completed
```

Ignore:

```text
non-workflow_job events
```

---

## 26. Phase 5 — Prefix + Profile Resolver

This is where the first new requirement becomes enforceable.

Pipeline:

```text
workflow_job
     ↓
labels
     ↓
prefix validation
     ↓
profile resolver
     ↓
RunnerProfile
```

Example:

```text
labels:
[
  self-hosted,
  erp-sport,
  linux,
  amd64
]

             ↓

prefix = erp-sport ✓

             ↓

profile = amd64
```

---

## 27. Phase 6 — Runner Group

Resolve:

```text
config.group
```

If:

```text
""
```

then:

```text
Default Runner Group
```

If:

```text
"aws-spot-runners"
```

then:

```text
aws-spot-runners
```

This should be applied **only during runner registration**.

---

## 28. Phase 7 — Runner Controller

Implement:

```go
ReconcileQueuedJob()
ReconcileInProgressJob()
ReconcileCompletedJob()
```

Core:

```text
queued
  ↓
allocate existing
  OR
provision new

in_progress
  ↓
BUSY

completed
  ↓
IDLE
  ↓
terminate_after = now + 5m
```

---

## 29. Phase 8 — EC2 Provisioning

Implement AWS SDK v2:

```text
RunInstances
DescribeInstances
TerminateInstances
```

Spot strategy:

```text
LaunchTemplate
+
Spot request / market options
```

UserData:

```text
install runner
download runner
registration
labels
runner group
start service
```

---

## 30. Phase 9 — Cleanup

Implement:

```text
EventBridge
    ↓
Cleanup Lambda
    ↓
DynamoDB
    ↓
expired IDLE runners
    ↓
atomic TERMINATING
    ↓
GitHub remove
    ↓
EC2 terminate
```

Also handle orphan recovery:

```text
DynamoDB says RUNNING
but EC2 does not exist
```

or:

```text
EC2 exists
but GitHub runner does not
```

These are reconciliation cases.

---

## 31. Phase 10 — Observability + Hardening

CloudWatch metrics:

```text
runner.provisioned
runner.provision_failed
runner.busy
runner.idle
runner.terminated
runner.reused

job.queued
job.waiting
job.completed

controller.reconcile
controller.error
```

Useful metrics:

```text
RunnerProvisionLatency
JobQueueLatency
RunnerReuseRate
RunnerIdleDuration
ProvisionFailureRate
OrphanRunnerCount
```

Alarms:

```text
SQS DLQ > 0

Provision failure > threshold

Controller Lambda errors

Job queue age too high

EC2 provisioning failures

GitHub API errors
```

---

## 32. Spot Interruption

Enterprise architecture should handle:

```text
EC2 Spot
   ↓
2-minute interruption notice
   ↓
EventBridge / interruption handler
   ↓
runner → TERMINATING
   ↓
GitHub runner unavailable
   ↓
controller detects job still queued
   ↓
provision replacement
```

Do not try to keep the Spot instance when AWS has already prepared to terminate it.

---

## 33. Failure Scenarios

### Lambda runs twice

```text
DynamoDB conditional write
```

→ only one succeeds.

### GitHub webhook retry

```text
delivery_id
```

→ idempotent.

### EC2 created but registration fails

```text
PROVISIONING
      ↓
registration failed
      ↓
terminate EC2
      ↓
mark FAILED
```

### Runner dies before job completes

Controller:

```text
GitHub runner status
+
EC2 status
+
workflow_job state
```

reconcile.

### Cleanup race with new job

```text
Cleanup:
IDLE → TERMINATING
```

conditional update.

If job allocation has already:

```text
IDLE → BUSY
```

cleanup fail condition.

---

## 34. Final State Model

I recommend the final state machine:

```text
                    ┌──────────────┐
                    │ PROVISIONING │
                    └──────┬───────┘
                           │
                           ▼
                    ┌──────────────┐
                    │   STARTING   │
                    └──────┬───────┘
                           │
                           ▼
                    ┌──────────────┐
              ┌────►│     IDLE     │◄─────┐
              │     └──────┬───────┘      │
              │            │              │
              │            │ allocate     │ job complete
              │            ▼              │
              │     ┌──────────────┐      │
              └─────│     BUSY     │──────┘
                    └──────────────┘
                           │
                     timeout
                           │
                           ▼
                    ┌──────────────┐
                    │ TERMINATING  │
                    └──────┬───────┘
                           │
                           ▼
                    ┌──────────────┐
                    │  TERMINATED  │
                    └──────────────┘
```

---

## 35. Final Configuration Example

```yaml
runner:
  prefix: "erp-sport"

  ## Empty = GitHub Default Runner Group
  group: "aws-spot-runners"

  idle_timeout: 5m

  profiles:

    amd64:
      labels:
        - linux
        - amd64

      architecture: x86_64
      ami: ami-xxxxxxxx
      instance_type: c7i.large

    arm64:
      labels:
        - linux
        - arm64

      architecture: arm64
      ami: ami-yyyyyyyy
      instance_type: c7g.large
```

Developer workflow:

```yaml
name: CI

on:
  push:
  pull_request:

jobs:

  build:
    runs-on:
      - self-hosted
      - erp-sport
      - linux
      - amd64

    steps:
      - uses: actions/checkout@v4

      - run: go test ./...

  test:
    needs: build

    runs-on:
      - self-hosted
      - erp-sport
      - linux
      - amd64

    steps:
      - run: go test ./...

  arm-build:
    runs-on:
      - self-hosted
      - erp-sport
      - linux
      - arm64

    steps:
      - run: go test ./...
```

---

## 36. Enterprise-Level Final Architecture

In summary, the system should be viewed as:

```text
                     GITHUB ACTIONS
                           │
                           │ workflow_job
                           ▼
                    ┌──────────────┐
                    │ API Gateway  │
                    └──────┬───────┘
                           │
                           ▼
                    ┌──────────────┐
                    │ Webhook      │
                    │ Lambda       │
                    └──────┬───────┘
                           │
                       SQS + DLQ
                           │
                           ▼
              ┌─────────────────────────┐
              │ Runner Controller       │
              │                         │
              │ Prefix validation       │
              │ Profile resolution      │
              │ Group resolution        │
              │ Capacity reconciliation │
              │ Runner allocation       │
              │ Provisioning            │
              └───────┬─────────┬───────┘
                      │         │
              ┌───────┘         └─────────┐
              ▼                           ▼
        ┌────────────┐             ┌──────────────┐
        │ DynamoDB   │             │ GitHub API   │
        │ state      │             │ App Auth     │
        │ lock       │             │ Runner API   │
        │ idempotency│             │ Group API    │
        └──────┬─────┘             └──────────────┘
               │
               ▼
        ┌──────────────────────┐
        │ AWS EC2 Spot         │
        │                      │
        │ ┌──────────────────┐ │
        │ │ x86_64 Runner    │ │
        │ └──────────────────┘ │
        │                      │
        │ ┌──────────────────┐ │
        │ │ ARM64 Runner     │ │
        │ └──────────────────┘ │
        └──────────────────────┘

               ▲
               │
        EventBridge
          3 minute
               │
               ▼
        Cleanup Lambda
               │
               ▼
        Expired IDLE runners
               │
               ▼
        GitHub remove
               │
               ▼
        EC2 terminate
```

**The two new requirements sit exactly at the control plane**, without complicating the workflow:

```text
                    workflow_job
                         │
                         ▼
                 ┌───────────────┐
                 │ Prefix check  │  ← Requirement #1
                 └───────┬───────┘
                         │
                         ▼
                 ┌───────────────┐
                 │ Profile       │
                 │ resolution    │
                 └───────┬───────┘
                         │
                         ▼
                 ┌───────────────┐
                 │ Runner Group  │  ← Requirement #2
                 │ resolution    │
                 └───────┬───────┘
                         │
                         ▼
                    Reconcile
```

**Recommended actual implementation order:** `Domain model → Config → GitHub App → Webhook/SQS → Prefix/Profile resolver → Runner Group → DynamoDB state/locking → Controller → EC2 provisioning → Cleanup → Spot recovery → Observability → integration tests`. This helps avoid writing EC2 provisioning before the scheduling/state model is stable.
