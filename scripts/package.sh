#!/usr/bin/env bash
# Zips each lambda/<name>/bootstrap (built by scripts/build.sh) into
# lambda/<name>.zip, which infrastructure/modules/lambda's archive_file data
# source reads. Run after build.sh, before `terraform plan`/`apply`.
set -euo pipefail

cd "$(dirname "$0")/.."

FUNCTIONS=(webhook provision cleanup)

for name in "${FUNCTIONS[@]}"; do
  bootstrap="lambda/${name}/bootstrap"
  if [[ ! -x "${bootstrap}" ]]; then
    echo "error: ${bootstrap} not found or not executable — run scripts/build.sh first" >&2
    exit 1
  fi

  zip_path="lambda/${name}.zip"
  echo "==> packaging ${zip_path}"
  rm -f "${zip_path}"
  (cd "lambda/${name}" && zip -q -j "../${name}.zip" bootstrap)
done

echo "==> package complete"
