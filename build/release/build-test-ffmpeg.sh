#!/usr/bin/env bash
# Build the fixture generator for acceptance tests; never install it into a YATM package.
set -euo pipefail
REPOSITORY="$(cd "$(dirname "$0")/../.." && pwd)"
OUTPUT_DIRECTORY="${1:?Usage: build-test-ffmpeg.sh OUTPUT_DIRECTORY}"
mkdir -p "$OUTPUT_DIRECTORY"
OUTPUT_DIRECTORY="$(cd "$OUTPUT_DIRECTORY" && pwd)"
command -v pkg-config >/dev/null
pkg-config --exists x264 x265 zlib
BUILD_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/yatm-test-ffmpeg.XXXXXX")"
trap 'rm -rf -- "$BUILD_DIRECTORY"' EXIT
ARCHIVE="$BUILD_DIRECTORY/ffmpeg-8.0.tar.xz"
if [[ -n "${FFMPEG_SOURCE_ARCHIVE:-}" ]]; then
  cp "$FFMPEG_SOURCE_ARCHIVE" "$ARCHIVE"
else
  curl --fail --location --retry 3 --output "$ARCHIVE" https://ffmpeg.org/releases/ffmpeg-8.0.tar.xz
fi
[[ "$(shasum -a 256 "$ARCHIVE" | cut -d ' ' -f 1)" == b2751fccb6cc4c77708113cd78b561059b6fa904b24162fa0be2d60273d27b8e ]] || {
  echo 'Fixture FFmpeg source checksum mismatch.' >&2; exit 1;
}
tar -xf "$ARCHIVE" -C "$BUILD_DIRECTORY"
cd "$BUILD_DIRECTORY/ffmpeg-8.0"
./configure --prefix="$OUTPUT_DIRECTORY" --disable-autodetect --disable-everything \
  --disable-doc --disable-debug --disable-x86asm --disable-ffprobe \
  --enable-ffmpeg --enable-gpl --enable-libx264 --enable-libx265 --enable-zlib \
  --enable-protocol=file --enable-indev=lavfi --enable-filter=testsrc2,color,format,scale \
  --enable-encoder=libx264,libx265,png --enable-decoder=wrapped_avframe \
  --enable-demuxer=mov,matroska --enable-muxer=mp4,mov,matroska,image2
make -j "${BUILD_JOBS:-4}" ffmpeg
mkdir -p "$OUTPUT_DIRECTORY/bin"
cp ffmpeg "$OUTPUT_DIRECTORY/bin/ffmpeg"
PATH="$OUTPUT_DIRECTORY/bin:$PATH" node "$REPOSITORY/build/release/check-preview-fixtures.mjs"
printf 'Fixture generator ready: %s/bin/ffmpeg\n' "$OUTPUT_DIRECTORY"
