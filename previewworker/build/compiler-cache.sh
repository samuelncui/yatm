#!/usr/bin/env bash
# Native compiler caching is optional; Go always uses the underlying compiler.

native_cache_key() {
  {
    printf '%s\n' "$TARGET" "$CC" "$CXX"
    uname -smr
    $CC --version
    $CXX --version
    if [[ "$TARGET" == darwin-* ]]; then
      xcrun --sdk macosx --show-sdk-version
      xcrun --sdk macosx --show-sdk-build-version
    fi
    for recipe in previewworker/build/build.sh previewworker/build/compiler-cache.sh previewworker/build/ffmpeg-sysctl-header.patch build/release/sanitize-native.mjs; do
      shasum -a 256 "$REPOSITORY/$recipe" | cut -d ' ' -f 1
    done
  } | shasum -a 256 | cut -d ' ' -f 1
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  set -euo pipefail
  [[ "${1:-}" == key ]] || { echo 'Usage: compiler-cache.sh key' >&2; exit 1; }
  REPOSITORY="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
  TARGET="${PREVIEW_TARGET:-$(go env GOHOSTOS)-$(go env GOHOSTARCH)}"
  CC="${PREVIEW_CC:-cc}" CXX="${PREVIEW_CXX:-c++}"
  native_cache_key
else
  NATIVE_CC="$CC" NATIVE_CXX="$CXX"
  if [[ -n "${CCACHE_DIR:-}" ]] && command -v ccache >/dev/null; then
    export CCACHE_BASEDIR="$BUILD_DIRECTORY" CCACHE_COMPILERCHECK=content
    export CCACHE_NAMESPACE="yatm-preview-$(native_cache_key)"
    NATIVE_CC="ccache $CC" NATIVE_CXX="ccache $CXX"
  fi
fi
