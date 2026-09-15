# Cost Estimate — 2026

Cost breakdown for the `main` environment (region: `ap-southeast-1`), based on the
architecture in [enterprise-standard-upgrade.md](./enterprise-standard-upgrade.md) and
the deployed configuration in `configs/runner.yaml`.

Rates were pulled directly from the AWS Price List API (not scraped estimates) —
see the [Appendix](#appendix-per-unit-pricing--sources) for raw rates and query commands.

## TL;DR

| | |
|---|---|
| **Estimated cost** | ~$3.50–$5.10/month core + a small, usage-dependent data-transfer line |
| Assumes | 300 CI jobs/month, ~10 min each, on `t4g.medium` On-Demand runners |
| Biggest fixed line item | Secrets Manager — $0.80/mo just to store 2 secrets |
| Biggest variable line item | EC2 runner-hours — still only $2–3.50/mo at this volume |
| Biggest cost *avoided* | No NAT Gateway — public subnets save ~$32+/mo vs. a comparable NAT-based design |

This is a **sub-$100/year system** at current volume. It only becomes material if job
volume grows ~50–100x, or the instance mix shifts to `t3.large`/heavier while staying
On-Demand.

## Usage assumptions

| Assumption | Value |
|---|---|
| Jobs/month | 300 |
| Avg job duration | ~10 minutes |
| Runner profile mix | 100% `t4g.medium`, On-Demand, `arm64` |
| Pricing model | On-Demand (matches `configs/runner.yaml`; no profile has `spot: true`) |
| Region | `ap-southeast-1` |

Base EC2 runtime = 300 jobs × 10 min = **50 hours/month**. Runners aren't ephemeral —
each one that isn't reused by a following queued job also accrues its 5-minute
`idle_timeout` tail plus ~1 minute of boot/registration overhead. That's why every
number below is a range: **low** = pure job time with full reuse, **high** = zero
reuse. Actual reuse rate isn't known yet, so treat "high" as the safer number to
budget against.

## Monthly cost breakdown

| Service | What it's for | Low | High |
|---|---|---:|---:|
| EC2 compute | Runner instance-hours (50–80 hrs × $0.0424) | $2.12 | $3.39 |
| EC2 public IPv4 | Auto-assigned public IP per runner | $0.25 | $0.40 |
| EBS gp3 | 8GB runner root volume, prorated | $0.05 | $0.08 |
| SQS | Job queue — cost driven by Lambda's long-poll, not job count | $0.20 | $0.35 |
| Secrets Manager | 2 flat secrets + ~1,800 API calls/mo | $0.81 | $0.81 |
| CloudWatch Logs | ~30–50MB/mo ingested, 30-day retention | $0.02 | $0.04 |
| Lambda + API Gateway + DynamoDB + EventBridge | Webhook intake, provisioning, state, cleanup | $0.00 | $0.00 |
| **Subtotal (core infra)** | | **$3.46** | **$5.08** |
| Data transfer out | Workload-dependent — see note below | $0 | $2–5+ |

**Why the orchestration services round to $0:** at this volume, usage sits entirely
inside Lambda's *permanent* free tier (1M requests + 400,000 GB-seconds/month —
this recurs every month, it's not a 12-month intro offer), API Gateway's ~900
requests/month cost well under a cent, and DynamoDB storage stays under the 25GB
free tier.

**Data transfer — the one line that can't be sized precisely:** AWS gives
100GB/month free data transfer out. At 300 jobs/month, egress would need to average
>330MB/job to spill into the paid tier at all. Typical CI (checkout + dependency
install) is usually well under that; jobs pulling Docker images or large artifacts
could push this to a few dollars/month — not knowable without profiling actual job
content.

## Re-estimating with different numbers

If actual job volume, average duration, or profile mix differs from the assumptions
above:

- EC2 compute, public IPv4, and EBS rows scale **linearly with runner-hours**.
- Lambda, SQS, and DynamoDB rows scale **roughly linearly with job count**.
- Everything else in "Subtotal (core infra)" is a **near-fixed cost** at any volume
  below a few thousand jobs/month.

## Appendix: per-unit pricing & sources

<details>
<summary>Full per-service unit rates for <code>ap-southeast-1</code></summary>

| Service | Unit | Rate |
|---|---|---|
| EC2 On-Demand — `t4g.medium` (primary profile) | per hour | **$0.0424** |
| EC2 On-Demand — `t3.small` | per hour | $0.0212 |
| EC2 On-Demand — `t3.medium` | per hour | $0.0528 |
| EC2 On-Demand — `t3.large` | per hour | $0.1056 |
| EC2 On-Demand — `t4g.small` | per hour | $0.0212 |
| EC2 On-Demand — `t4g.large` | per hour | $0.0848 |
| EC2 public IPv4 (auto-assigned, in-use) | per instance-hour | $0.0050 |
| EBS gp3 (runner root volume) | per GB-month | $0.0960 |
| Lambda (arm64) — requests | per million | $0.20 |
| Lambda (arm64) — duration, Tier 1 | per GB-second | $0.0000133334 |
| API Gateway HTTP API | per million requests (≤300M/mo) | $1.25 |
| SQS Standard | per million requests, Tier 1 | $0.40 |
| DynamoDB on-demand — writes | per million WRU | $0.71 |
| DynamoDB on-demand — reads | per million RRU | $0.1425 |
| DynamoDB storage | per GB-month (first 25GB free) | $0.285 |
| CloudWatch Logs — ingestion | per GB | $0.70 |
| CloudWatch Logs — storage | per GB-month | $0.03 |
| Secrets Manager | per secret/month | $0.40 |
| Secrets Manager — API calls | per 10,000 calls | $0.05 |
| EventBridge (default-bus rule → Lambda) | — | $0 (not a billed event type) |
| Data transfer out to internet | per GB, beyond 100GB free tier/mo | $0.12 |

</details>

<details>
<summary>Sources</summary>

Unit rates queried directly from the AWS Price List API on 2026-09-14, all filtered
to `location = "Asia Pacific (Singapore)"`:

```text
aws pricing get-products --service-code AmazonEC2 --region us-east-1 --filters '...'
aws pricing get-products --service-code AWSLambda --region us-east-1 --filters '...'
aws pricing get-products --service-code AmazonApiGateway --region us-east-1 --filters '...'
aws pricing get-products --service-code AmazonDynamoDB --region us-east-1 --filters '...'
aws pricing get-products --service-code AWSQueueService --region us-east-1 --filters '...'
aws pricing get-products --service-code AWSSecretsManager --region us-east-1 --filters '...'
aws pricing get-products --service-code AWSEvents --region us-east-1 --filters '...'
aws pricing get-products --service-code AmazonCloudWatch --region us-east-1 --filters '...'
aws pricing get-products --service-code AmazonVPC --region us-east-1 --filters '...'
aws pricing get-products --service-code AWSDataTransfer --region us-east-1 --filters '...'
```

Prices are point-in-time and subject to change — re-run these queries before relying
on this report for budget approval.

</details>
