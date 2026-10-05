#!/usr/bin/env bash
# Sourced by the backend build; Linux release binaries always use C SQLite.
: "${ZIG:=zig}"
if [[ "$("$ZIG" version)" != 0.15.2 ]]; then
  echo 'Linux builds require Zig 0.15.2 (https://ziglang.org/download/).' >&2
  exit 1
fi
export GOOS=linux GOARCH="${GOARCH:-$(go env GOARCH)}" CGO_ENABLED=1
ZIG_CPU=
case "$GOARCH" in
  amd64) ZIG_TARGET=x86_64-linux-musl ;;
  386) ZIG_TARGET=x86-linux-musl ;;
  arm64) ZIG_TARGET=aarch64-linux-musl ;;
  arm)
    export GOARM="${GOARM:-7}"
    case "$GOARM" in
      5) echo 'Unsupported Linux GOARM=5: Zig 0.15.2 cannot link the ARMv5 __sync atomics required by Go CGO; no pure-Go fallback is permitted.' >&2; exit 1 ;;
      6) ZIG_TARGET=arm-linux-musleabihf; ZIG_CPU=arm1176jzf_s ;;
      7) ZIG_TARGET=arm-linux-musleabihf; ZIG_CPU=cortex_a7 ;;
      *) echo "Unsupported Linux GOARM: $GOARM (expected 5, 6 or 7)" >&2; exit 1 ;;
    esac
    ;;
  s390x) ZIG_TARGET=s390x-linux-musl ;;
  *) echo "Unsupported Linux GOARCH: $GOARCH" >&2; exit 1 ;;
esac
export CC="$ZIG cc -target $ZIG_TARGET${ZIG_CPU:+ -mcpu=$ZIG_CPU}"
export CXX="$ZIG c++ -target $ZIG_TARGET${ZIG_CPU:+ -mcpu=$ZIG_CPU}"
