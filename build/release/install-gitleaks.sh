#!/usr/bin/env bash
set -euo pipefail

# Pin both the scanner and its published archive checksum.
destination="${1:?Usage: install-gitleaks.sh <destination-directory>}"
version=8.30.1
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) target=linux_x64; checksum=551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb ;;
  Darwin-arm64) target=darwin_arm64; checksum=b40ab0ae55c505963e365f271a8d3846efbc170aa17f2607f13df610a9aeb6a5 ;;
  Darwin-x86_64) target=darwin_x64; checksum=dfe101a4db2255fc85120ac7f3d25e4342c3c20cf749f2c20a18081af1952709 ;;
  *) echo 'Unsupported scanner host' >&2; exit 1 ;;
esac
temporary="$(mktemp -d "${TMPDIR:-/tmp}/yatm-gitleaks.XXXXXX")"
trap 'rm -rf -- "$temporary"' EXIT
archive="$temporary/gitleaks.tar.gz"
curl --fail --location --silent --show-error --output "$archive" \
  "https://github.com/gitleaks/gitleaks/releases/download/v$version/gitleaks_${version}_$target.tar.gz"
printf '%s  %s\n' "$checksum" "$archive" | shasum -a 256 --check
mkdir -p "$destination"
tar -xzf "$archive" -C "$destination" gitleaks
test "$("$destination/gitleaks" version)" = "$version"
curl --fail --location --silent --show-error --output "$temporary/default.toml" \
  "https://raw.githubusercontent.com/gitleaks/gitleaks/v$version/config/gitleaks.toml"
printf '%s  %s\n' e163e53b9e7e8a8511e77271e2b323ed057759542a6d988258afe3a1fa329caf "$temporary/default.toml" | shasum -a 256 --check
cp "$temporary/default.toml" "$destination/gitleaks-default.toml"
