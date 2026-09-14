# enterprise-standard-upgrade

Dưới đây là **complete architecture + implementation workflow** cho `github-runner-provisioner`, đã tích hợp **2 requirement mới**:

1. **Runner labels trong workflow bắt buộc phải chứa configurable prefix**.
2. **Platform có thể cấu hình GitHub Runner Group**; `empty` = GitHub Default Runner Group.
3. this repository is onetime setup, end user can configure via yaml file before applying to AWS infrastructure
4. Use golang for all lambda function
5. EC2 can be provisioned to the existing infrastructure or deploy new infrastructure
6. the final structure: Lamda function controller will receive the event from github via github apps, never call to github to proceed the ec2. Only allow to request GitHub to get github runner token via github app.

Mục tiêu kiến trúc là **enterprise-grade, event-driven, idempotent, scalable**, đồng thời vẫn giữ mô hình **runner pool + 5-minute idle reuse**.

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

Workflow **không gọi API provision runner**.

Developer chỉ khai báo capability:

```yaml
jobs:
  build:
    runs-on:
      - self-hosted
      - erp-sport
      - linux
      - amd64
```

Platform sẽ quyết định:

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

Developer **không được biết hoặc quyết định**:

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

Đây là separation giữa:

```text
Developer intent
        ↓
Platform policy
        ↓
AWS infrastructure
```

---

## 3. Requirement #1 — Runner Prefix

Đây là requirement rất quan trọng để tránh controller bắt nhầm job của runner platform khác.

### Configuration

Ví dụ:

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

Controller nhận:

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

Nếu:

```yaml
runs-on:
  - self-hosted
  - linux
  - amd64
```

thì:

```text
prefix = erp-sport
        ↓
not found
        ↓
IGNORE
        ↓
DO NOT PROVISION
```

#### Không nên dùng prefix như partial match

Không nên:

```text
strings.Contains(label, "erp-sport")
```

Vì:

```text
erp-sport
erp-sport-dev
my-erp-sport
```

sẽ gây ambiguity.

Nên exact-match:

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

`group: ""` nghĩa là:

```text
GitHub Default Runner Group
```

Nếu:

```yaml
runner:
  prefix: "erp-sport"
  group: "aws-spot-runners"
```

thì runner phải được register vào:

```text
aws-spot-runners
```

Ví dụ:

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

**Runner group không nên nằm trong `runs-on`.**

Không làm:

```yaml
runs-on:
  - self-hosted
  - erp-sport
  - aws-spot-runners
  - amd64
```

Developer chỉ yêu cầu:

```yaml
runs-on:
  - self-hosted
  - erp-sport
  - linux
  - amd64
```

Platform quyết định runner group.

---

## 5. Configuration Model

Tôi recommend cấu hình như sau:

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

Sau này có thể:

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

Controller map:

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

Runner không dùng `--ephemeral`.

Vì requirement hiện tại là **reuse trong 5 phút**.

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

=> **Chỉ cần 1 EC2.**

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

Controller thấy:

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

Đây là phần quan trọng nhất.

Controller không đơn giản:

```text
job queued → create EC2
```

Mà phải:

```text
GitHub event
     ↓
Desired state
     ↓
Current state
     ↓
Reconcile
```

Công thức:

```text
required capacity
    -
available capacity
    -
pending capacity
    =
new capacity required
```

Ví dụ:

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

Hai Lambda có thể chạy đồng thời:

```text
Lambda A ─────┐
              ├──→ same queued job
Lambda B ─────┘
```

Không được để cả hai provision runner.

DynamoDB conditional update:

```text
IDLE
 ↓
BUSY
```

chỉ một Lambda được phép thành công.

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

Lambda còn lại:

```text
ConditionalCheckFailed
        ↓
runner đã được allocate
        ↓
reconcile lại
```

---

## 11. DynamoDB Model

Tôi recommend tách entity rõ ràng.

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

GitHub webhook có thể retry.

Do đó:

```text
delivery_id
```

phải được lưu.

Ví dụ:

```text
pk = EVENT#github-delivery-id
```

Nếu event đã tồn tại:

```text
duplicate
   ↓
ACK
   ↓
do nothing
```

Không provision lần nữa.

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

Không nên hard-code:

```go
if arch == "amd64" {
    instanceType = "c7i.large"
}
```

Tách thành resolver:

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

Ví dụ:

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

Nếu:

```text
group = ""
```

thì **không truyền runner group override**, để GitHub sử dụng Default group.

---

## 18. GitHub App Authentication

Không dùng PAT.

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

Lambda chỉ cần permission:

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

Quan trọng: trước khi terminate phải re-check.

Ví dụ:

```text
22:00 runner IDLE
terminate_after = 22:05

22:04:59
new job arrives
runner → BUSY
```

Cleanup lúc:

```text
22:05
```

phải thấy:

```text
status != IDLE
```

→ không terminate.

---

## 21. GitHub Runner Group Validation

Khi config:

```yaml
group: "aws-spot-runners"
```

controller nên validate group tồn tại trước khi provision.

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

Không nên để:

```text
EC2 created
   ↓
runner registration failed
   ↓
orphan EC2
```

Có thể cache group ID/name trong DynamoDB/config cache.

---

## 22. Repository / Organization Scope

Config nên hỗ trợ:

```yaml
runner:
  prefix: "erp-sport"
  group: ""
  scope: repository
```

hoặc:

```yaml
runner:
  scope: organization
```

Ví dụ repository:

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

Controller phải giữ scope trong DynamoDB để tránh allocate nhầm runner giữa các repository.

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

Tôi recommend implement theo **10 phases**, không làm tất cả cùng lúc.

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

Enterprise architecture nên xử lý:

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

Không cố giữ Spot instance khi AWS đã chuẩn bị terminate.

---

## 33. Failure Scenarios

### Lambda chạy 2 lần

```text
DynamoDB conditional write
```

→ only one succeeds.

### GitHub webhook retry

```text
delivery_id
```

→ idempotent.

### EC2 created nhưng registration fail

```text
PROVISIONING
      ↓
registration failed
      ↓
terminate EC2
      ↓
mark FAILED
```

### Runner chết trước khi job complete

Controller:

```text
GitHub runner status
+
EC2 status
+
workflow_job state
```

reconcile.

### Cleanup race với new job

```text
Cleanup:
IDLE → TERMINATING
```

conditional update.

Nếu job allocation đã:

```text
IDLE → BUSY
```

cleanup fail condition.

---

## 34. Final State Model

Tôi recommend final state machine:

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

Tóm lại, hệ thống nên được nhìn như:

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

**Hai requirement mới nằm đúng ở control plane**, không làm workflow phức tạp:

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

**Thứ tự implement thực tế tôi khuyên:** `Domain model → Config → GitHub App → Webhook/SQS → Prefix/Profile resolver → Runner Group → DynamoDB state/locking → Controller → EC2 provisioning → Cleanup → Spot recovery → Observability → integration tests`. Điều này giúp tránh việc viết EC2 provisioning trước khi scheduling/state model ổn định.
