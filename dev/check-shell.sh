#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname -- "${BASH_SOURCE[0]}")/.."
# Check repository shell sources and extensionless runtime/test adapters, never local deployment scripts.
while IFS= read -r -d '' script; do
  bash -n "$script"
done < <(find build dev frontend/scripts previewworker/build scripts e2e/testdata e2e/ltfs-file-backend -type f \( -name '*.sh' -o ! -name '*.*' \) -print0)
bash -n install-release.sh
