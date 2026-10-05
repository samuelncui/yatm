#!/usr/bin/env bash
# Collect the frontend once, optionally reusing the dist produced by source preflight.
set -euo pipefail
REPOSITORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
: "${1:?Usage: build-frontend.sh <new-artifact-directory> [checked-dist-directory]}"
OUTPUT_DIRECTORY="$1"
[[ "$OUTPUT_DIRECTORY" == /* ]] || OUTPUT_DIRECTORY="$PWD/$OUTPUT_DIRECTORY"
DIST_DIRECTORY="${2:-}"
[[ -z "$DIST_DIRECTORY" || "$DIST_DIRECTORY" == /* ]] || DIST_DIRECTORY="$PWD/$DIST_DIRECTORY"
[[ ! -e "$OUTPUT_DIRECTORY" ]] || { echo 'Frontend artifact directory must be new' >&2; exit 1; }
export OUTPUT_DIRECTORY
cd "$REPOSITORY"
if [[ -n "$DIST_DIRECTORY" ]]; then
  mkdir -p "$OUTPUT_DIRECTORY/frontend"
  cp -R "$DIST_DIRECTORY/." "$OUTPUT_DIRECTORY/frontend/"
else
  bash frontend/scripts/build.sh
fi
node build/release/build-licenses.mjs "$OUTPUT_DIRECTORY/licenses" frontend
node build/release/frontend-artifact.mjs create "$OUTPUT_DIRECTORY"
