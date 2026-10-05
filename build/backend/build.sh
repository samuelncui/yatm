#!/usr/bin/env bash
set -euo pipefail

REPOSITORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
OUTPUT_DIRECTORY="${OUTPUT_DIRECTORY:-$REPOSITORY/output}"
case "$OUTPUT_DIRECTORY" in /*) ;; *) OUTPUT_DIRECTORY="$PWD/$OUTPUT_DIRECTORY" ;; esac
mkdir -p "$OUTPUT_DIRECTORY"
export OUTPUT_DIRECTORY
cd "$REPOSITORY"
mkdir -p "$OUTPUT_DIRECTORY"
RELEASE_VERSION="${RELEASE_VERSION:-development}"
RELEASE_COMMIT="${RELEASE_COMMIT:-$(git rev-parse HEAD)}"
LDFLAGS="-X github.com/samuelncui/yatm/internal/buildinfo.Version=$RELEASE_VERSION -X github.com/samuelncui/yatm/internal/buildinfo.Commit=$RELEASE_COMMIT"
EXTERNAL_LDFLAGS="-static"

# Distributed binaries omit both Go and native debug data, including compiler/cache paths.
if [[ "$RELEASE_VERSION" != development ]]; then
  LDFLAGS="$LDFLAGS -s -w"
  EXTERNAL_LDFLAGS="$EXTERNAL_LDFLAGS -Wl,--strip-all"
fi

if [[ "${GOOS:-$(go env GOOS)}" == linux ]]; then
  source build/backend/linux-env.sh
  LDFLAGS="$LDFLAGS -linkmode external -extldflags '$EXTERNAL_LDFLAGS'"
fi

for command in httpd yatm-cli export-library lto-info migrate; do
  program="yatm-$command"
  [[ "$command" != yatm-cli ]] || program=yatm-cli
  go build -trimpath -ldflags "$LDFLAGS" -o "$OUTPUT_DIRECTORY/$program" "./cmd/$command"
  if [[ "${GOOS:-$(go env GOOS)}" == linux ]]; then
    node build/release/check-static-linux.mjs "$OUTPUT_DIRECTORY/$program"
  fi
done
