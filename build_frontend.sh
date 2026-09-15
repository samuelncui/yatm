#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
OUTPUT_DIRECTORY="${OUTPUT_DIRECTORY:-$PWD/output}"
export VITE_YATM_VERSION="${RELEASE_VERSION:-development}"
export VITE_YATM_COMMIT="${RELEASE_COMMIT:-$(git rev-parse HEAD)}"
cd frontend
CI=true pnpm install --frozen-lockfile --registry=https://registry.npmjs.org
pnpm run build
mkdir -p "$OUTPUT_DIRECTORY/frontend"
cp -R dist/. "$OUTPUT_DIRECTORY/frontend/"
