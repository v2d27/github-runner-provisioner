variable "environment" {
  type        = string
  description = "Short environment name (e.g. \"dev\", \"prod\") — used only for naming/tagging, never to branch behavior."
}

variable "project_name" {
  type    = string
  default = "provision-github-runner"
}

# --- Runtime config, handed straight to every Lambda -----------------------

variable "runner_config_json" {
  type        = string
  description = "Pre-rendered JSON of configs/runner.yaml's github/webhook/runner sections (the infrastructure section is Terraform/Terragrunt-only and never included here). Passed verbatim as the RUNNER_CONFIG_JSON environment variable."
  sensitive   = false
}

variable "github_app_private_key_secret_name" {
  type        = string
  description = "Must match runner_config_json's github.private_key_secret_name."
}

variable "webhook_secret_name" {
  type        = string
  description = "Must match runner_config_json's webhook.secret_name."
}

variable "github_app_private_key" {
  type        = string
  description = "The GitHub App's PEM private key, from configs/secrets.yaml's github.private_key_secret_value. Written as the value of the github_app_private_key_secret_name secret."
  sensitive   = true
}

variable "webhook_secret_value" {
  type        = string
  description = "The webhook's HMAC signing secret, from configs/secrets.yaml's webhook.secret_value. Written as the value of the webhook_secret_name secret."
  sensitive   = true
}

# --- Lambda build artifacts --------------------------------------------------

variable "build_dir" {
  type        = string
  description = "Absolute path to the directory containing webhook.zip/provision.zip/cleanup.zip (produced by scripts/build.sh + scripts/package.sh). Must be absolute — Terragrunt runs this module from a cache directory, not its source location, so a relative path would resolve to the wrong place."
}

# --- Existing-vs-new VPC (nullable — see ec2.tf) -----------------------------

variable "existing_vpc_id" {
  type    = string
  default = null
}

variable "existing_subnet_ids" {
  type    = list(string)
  default = null
}

variable "existing_security_group_id" {
  type    = string
  default = null
}

variable "vpc_cidr_block" {
  type    = string
  default = "10.42.0.0/16"
}

variable "subnet_cidr_blocks" {
  type    = list(string)
  default = ["10.42.1.0/24", "10.42.2.0/24"]
}

variable "allow_egress_cidr_blocks" {
  type        = list(string)
  default     = ["0.0.0.0/0"]
  description = "Runners need outbound HTTPS to github.com and package repositories; no inbound access is required."
}

# --- Tunables -----------------------------------------------------------------

variable "dynamodb_point_in_time_recovery_enabled" {
  type    = bool
  default = true
}

variable "webhook_lambda_timeout" {
  type    = number
  default = 10
}

variable "provision_lambda_timeout" {
  type    = number
  default = 60
}

variable "cleanup_lambda_timeout" {
  type    = number
  default = 60
}

variable "sqs_max_receive_count" {
  type        = number
  default     = 5
  description = "Deliveries before a message moves to the DLQ."
}

variable "cleanup_schedule_expression" {
  type    = string
  default = "rate(3 minutes)"
}

variable "enable_spot_interruption_rule" {
  type        = bool
  default     = true
  description = "React to EC2 Spot interruption notices immediately instead of waiting for the next scheduled sweep."
}

variable "log_retention_in_days" {
  type    = number
  default = 30
}

variable "tags" {
  type    = map(string)
  default = {}
}
