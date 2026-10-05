#!/usr/bin/env bash
set -euo pipefail

export GOOS=linux
export GOARCH="${GOARCH:-amd64}"
exec "$(dirname -- "${BASH_SOURCE[0]}")/build.sh"
