#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

# Invalid/trailing arguments must be rejected before any installation effects.
for arguments in '--version' '--version --help' '--version v0.1.21 --unknown' '--help --unknown' '--version='; do
  if bash -c 'source ./install-release.sh; judgment_parameters "$@"' test $arguments; then
    echo "Unexpected accepted arguments: $arguments" >&2
    exit 1
  fi
done
bash -c 'source ./install-release.sh; judgment_parameters --version v0.1.21 --help; [[ "$VERSION" == v0.1.21 && "$HELP" == 1 ]]'

# Stable-to-stable remains supported; cross-major and unknown installations stop.
for target in v1.0.0-alpha.1 v1.0.0 v2.0.0 unknown; do
  if bash -c 'source ./install-release.sh; CURRENT_VERSION=v0.1.8; RELEASE_VERSION="$1"; validate_legacy_upgrade' test "$target"; then
    exit 1
  fi
done
for current in v1.0.0-alpha.1 unknown; do
  if bash -c 'source ./install-release.sh; CURRENT_VERSION="$1"; RELEASE_VERSION=v0.1.21; validate_legacy_upgrade' test "$current"; then
    exit 1
  fi
done
bash -c 'source ./install-release.sh; CURRENT_VERSION=v0.1.8; RELEASE_VERSION=v0.1.21; validate_legacy_upgrade'
echo 'Legacy installer guards passed.'
