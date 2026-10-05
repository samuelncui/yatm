#!/usr/bin/env bash
set -euo pipefail

REPOSITORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
OUTPUT_DIRECTORY="${OUTPUT_DIRECTORY:-$REPOSITORY/output}"
case "$OUTPUT_DIRECTORY" in /*) ;; *) OUTPUT_DIRECTORY="$PWD/$OUTPUT_DIRECTORY" ;; esac
mkdir -p "$OUTPUT_DIRECTORY"
export OUTPUT_DIRECTORY
cd "$REPOSITORY"
export VITE_YATM_VERSION="${RELEASE_VERSION:-development}"
export VITE_YATM_COMMIT="${RELEASE_COMMIT:-$(git rev-parse HEAD)}"
cd frontend
CI=true pnpm install --frozen-lockfile --registry=https://registry.npmjs.org
pnpm run build
mkdir -p "$OUTPUT_DIRECTORY/frontend"
cp -R dist/. "$OUTPUT_DIRECTORY/frontend/"
