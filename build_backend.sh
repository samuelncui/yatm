#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"
OUTPUT_DIRECTORY="${OUTPUT_DIRECTORY:-$PWD/output}"
mkdir -p "$OUTPUT_DIRECTORY"
RELEASE_VERSION="${RELEASE_VERSION:-development}"
RELEASE_COMMIT="${RELEASE_COMMIT:-$(git rev-parse HEAD)}"
LDFLAGS="-X github.com/samuelncui/yatm/internal/buildinfo.Version=$RELEASE_VERSION -X github.com/samuelncui/yatm/internal/buildinfo.Commit=$RELEASE_COMMIT"

for command in httpd yatm-cli export-library lto-info migrate; do
  program="yatm-$command"
  [[ "$command" != yatm-cli ]] || program=yatm-cli
  go build -trimpath -ldflags "$LDFLAGS" -o "$OUTPUT_DIRECTORY/$program" "./cmd/$command"
done
