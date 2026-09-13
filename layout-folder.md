
github-runner-manager/
├── README.md
├── Makefile
├── go.mod
├── go.sum
│
├── cmd/
│   ├── webhook/
│   │   └── main.go
│   ├── provision/
│   │   └── main.go
│   └── cleanup/
│       └── main.go
│
├── internal/
│   ├── config/
│   │   └── config.go
│   │
│   ├── github/
│   │   ├── app.go
│   │   ├── client.go
│   │   ├── installation.go
│   │   ├── runners.go
│   │   └── webhook.go
│   │
│   ├── runner/
│   │   ├── service.go
│   │   ├── provision.go
│   │   ├── lifecycle.go
│   │   └── validation.go
│   │
│   ├── store/
│   │   ├── dynamodb.go
│   │   ├── runner.go
│   │   └── config.go
│   │
│   ├── webhook/
│   │   ├── handler.go
│   │   └── events.go
│   │
│   └── cleanup/
│       └── service.go
│
├── lambda/
│   ├── webhook/
│   │   └── bootstrap
│   ├── provision/
│   │   └── bootstrap
│   └── cleanup/
│       └── bootstrap
│
├── terraform/
│   ├── modules/
│   │   ├── lambda/
│   │   │   ├── main.tf
│   │   │   ├── variables.tf
│   │   │   └── outputs.tf
│   │   │
│   │   ├── dynamodb/
│   │   │   ├── main.tf
│   │   │   ├── variables.tf
│   │   │   └── outputs.tf
│   │   │
│   │   ├── eventbridge/
│   │   │   ├── main.tf
│   │   │   ├── variables.tf
│   │   │   └── outputs.tf
│   │   │
│   │   ├── iam/
│   │   │   ├── main.tf
│   │   │   ├── variables.tf
│   │   │   └── outputs.tf
│   │   │
│   │   ├── secrets/
│   │   │   ├── main.tf
│   │   │   ├── variables.tf
│   │   │   └── outputs.tf
│   │   │
│   │   └── s3/
│   │       ├── main.tf
│   │       ├── variables.tf
│   │       └── outputs.tf
│   │
│   ├── environments/
│   │   ├── dev/
│   │   │   ├── main.tf
│   │   │   ├── variables.tf
│   │   │   ├── terraform.tfvars
│   │   │   └── backend.tf
│   │   │
│   │   └── prod/
│   │       ├── main.tf
│   │       ├── variables.tf
│   │       ├── terraform.tfvars
│   │       └── backend.tf
│   │
│   └── versions.tf
│
├── configs/
│   ├── runner.yaml
│   └── repositories.yaml
│
├── scripts/
│   ├── build.sh
│   ├── package.sh
│   └── deploy.sh
│
├── .github/
│   └── workflows/
│       ├── terraform.yml
│       └── build.yml
│
└── .gitignore 

