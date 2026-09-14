.PHONY: build package deploy-plan deploy-apply fmt vet test tf-fmt tf-validate clean

build:
	./scripts/build.sh

package: build
	./scripts/package.sh

deploy-plan: package
	./scripts/deploy.sh plan

deploy-apply: package
	./scripts/deploy.sh apply

fmt:
	gofmt -l .

vet:
	go vet ./...

test:
	go test ./...

tf-fmt:
	terragrunt hcl fmt --working-dir infrastructure --check
	terraform fmt -recursive -check infrastructure/modules

# Credential-free: validates the Terragrunt HCL itself, then the Terraform
# module directly (no provider config or state backend needed — see
# infrastructure/modules/github-runner-on-aws). This does NOT need a real S3
# state bucket; `terragrunt validate` (which does) only runs as part of
# `make deploy-plan`/`deploy-apply`.
tf-validate:
	terragrunt hcl validate --working-dir infrastructure
	cd infrastructure/modules/github-runner-on-aws && terraform init -backend=false -input=false >/dev/null && terraform validate
	rm -rf infrastructure/modules/github-runner-on-aws/.terraform infrastructure/modules/github-runner-on-aws/.terraform.lock.hcl

clean:
	rm -rf lambda/*/bootstrap lambda/*.zip
