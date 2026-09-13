#!/usr/bin/env bash
# Builds each cmd/<name> as a Lambda "provided.al2023" custom-runtime
# bootstrap binary into lambda/<name>/bootstrap.
#
# Must run before any `terraform plan`/`apply` — terraform/modules/lambda's
# archive_file reads this output directly and never builds it itself
# (a null_resource/local-exec calling `go build` from inside Terraform would
# make `plan` non-deterministic and side-effecting).
set -euo pipefail

cd "$(dirname "$0")/.."

FUNCTIONS=(webhook provision cleanup)
GOARCH="${GOARCH:-arm64}" # must match terraform/modules/lambda's `architecture` variable

for name in "${FUNCTIONS[@]}"; do
  echo "==> building ${name} (linux/${GOARCH})"
  mkdir -p "lambda/${name}"
  GOOS=linux GOARCH="${GOARCH}" CGO_ENABLED=0 \
    go build -trimpath -ldflags="-s -w" -o "lambda/${name}/bootstrap" "./cmd/${name}"
  chmod +x "lambda/${name}/bootstrap"
done

echo "==> build complete"
