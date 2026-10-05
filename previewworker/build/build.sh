#!/usr/bin/env bash
# Build an optional, native Preview package without installing system libraries.
set -euo pipefail
REPOSITORY="$(cd "$(dirname "$0")/../.." && pwd)"
RELEASE_DIRECTORY="${RELEASE_DIRECTORY:-$REPOSITORY/output/releases}"
[[ "$RELEASE_DIRECTORY" == /* ]] || RELEASE_DIRECTORY="$PWD/$RELEASE_DIRECTORY"
if [[ -n "${PREVIEW_SOURCE_DIRECTORY:-}" && "$PREVIEW_SOURCE_DIRECTORY" != /* ]]; then
  PREVIEW_SOURCE_DIRECTORY="$PWD/$PREVIEW_SOURCE_DIRECTORY"
fi
if [[ -n "${CCACHE_DIR:-}" && "$CCACHE_DIR" != /* ]]; then
  export CCACHE_DIR="$PWD/$CCACHE_DIR"
fi
cd "$REPOSITORY"
: "${RELEASE_VERSION:?Set RELEASE_VERSION to the candidate tag}"
[[ "$RELEASE_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[A-Za-z0-9.-]+)?$ ]] || { echo 'Invalid release version' >&2; exit 1; }
DISTRIBUTED_SOURCES=false
if [[ ! -e .git ]]; then
  DISTRIBUTED_SOURCES=true
  # Keep generated output outside the verified corresponding-source tree.
  if [[ "$RELEASE_DIRECTORY" == "$REPOSITORY/output/releases" && -n "${PREVIEW_SOURCE_DIRECTORY:-}" ]]; then
    RELEASE_DIRECTORY="$PREVIEW_SOURCE_DIRECTORY/../output/releases"
  fi
fi
# Validate provenance before invoking compilers or fetching dependencies.
BUILD_DIRECTORY="$(mktemp -d "${TMPDIR:-/tmp}/yatm-preview-build.XXXXXX")"
BUILD_DIRECTORY="$(cd "$BUILD_DIRECTORY" && pwd -P)"
trap 'status=$?; if [[ "$status" == 0 ]]; then rm -rf -- "$BUILD_DIRECTORY"; else echo "Failed build retained at $BUILD_DIRECTORY" >&2; fi' EXIT
RELEASE_COMMIT="$(node "$REPOSITORY/build/release/committed-inputs.mjs" checkout "$REPOSITORY" "$BUILD_DIRECTORY/source" "${RELEASE_COMMIT:-}" "${PREVIEW_SOURCE_DIRECTORY:-}")"
export RELEASE_COMMIT
REPOSITORY="$BUILD_DIRECTORY/source"
cd "$REPOSITORY"
# Release objects carry no debugger paths; source-location strings use one public build root.
NATIVE_PATH_FLAGS="-ffile-prefix-map=$BUILD_DIRECTORY=/build/yatm-preview -fdebug-prefix-map=$BUILD_DIRECTORY=/build/yatm-preview"
export CFLAGS="${CFLAGS:--O2} -g0 $NATIVE_PATH_FLAGS"
export CXXFLAGS="${CXXFLAGS:--O2} -g0 $NATIVE_PATH_FLAGS"
export CGO_CFLAGS="${CGO_CFLAGS:--O2} -g0 $NATIVE_PATH_FLAGS"
export CGO_CXXFLAGS="${CGO_CXXFLAGS:--O2} -g0 $NATIVE_PATH_FLAGS"
HOST_TARGET="$(go env GOHOSTOS)-$(go env GOHOSTARCH)"
TARGET="${PREVIEW_TARGET:-$HOST_TARGET}"
GOOS="${TARGET%-*}"
GOARCH="${TARGET#*-}"
case "$GOOS-$GOARCH" in
  linux-amd64) command -v patchelf >/dev/null ;;
  darwin-amd64|darwin-arm64) command -v install_name_tool >/dev/null ;;
  *) echo "Unsupported Preview target: $TARGET" >&2; exit 1 ;;
esac
export GOOS GOARCH CGO_ENABLED=1
if [[ "$GOOS" == darwin ]]; then
  export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-15.0}"
fi
export CC="${PREVIEW_CC:-cc}" CXX="${PREVIEW_CXX:-c++}"
WEBP_TARGET=()
FFMPEG_TARGET=()
RUNNABLE=true
if [[ "$TARGET" != "$HOST_TARGET" ]]; then
  case "$HOST_TARGET:$TARGET" in
    darwin-arm64:darwin-amd64)
      arch -x86_64 /usr/bin/true || { echo 'Intel macOS helper validation requires Rosetta.' >&2; exit 1; }
      export CC='clang -arch x86_64' CXX='clang++ -arch x86_64'
      WEBP_TARGET=(--host=x86_64-apple-darwin)
      FFMPEG_TARGET=(--enable-cross-compile --target-os=darwin --arch=x86_64)
      ;;
    *:linux-amd64)
      [[ "$(zig version)" == 0.15.2 ]] || { echo 'Cross compilation requires Zig 0.15.2.' >&2; exit 1; }
      command -v llvm-objcopy >/dev/null
      export CC='zig cc -target x86_64-linux-gnu.2.35' CXX='zig c++ -target x86_64-linux-gnu.2.35'
      export AR='zig ar' RANLIB='zig ranlib' LD='zig ld.lld' CHOST=x86_64-linux-gnu
      WEBP_TARGET=(--host=x86_64-linux-gnu)
      FFMPEG_TARGET=(--enable-cross-compile --target-os=linux --arch=x86_64 --ar='zig ar' --ranlib='zig ranlib')
      RUNNABLE=false
      ;;
    *) echo "Unsupported Preview cross target: $HOST_TARGET to $TARGET" >&2; exit 1 ;;
  esac
fi
source "$REPOSITORY/previewworker/build/compiler-cache.sh"
command -v pkg-config >/dev/null
if [[ "$RUNNABLE" == true ]]; then
  node build/release/check-preview-fixtures.mjs
fi
PREFIX="$BUILD_DIRECTORY/native"
PACKAGE="$BUILD_DIRECTORY/package"
mkdir -p "$PREFIX" "$PACKAGE/preview-lib" "$PACKAGE/licenses" "$PACKAGE/sources/yatm"
JOBS="${BUILD_JOBS:-4}"
[[ "$JOBS" =~ ^[1-9][0-9]*$ ]] || { echo 'Invalid BUILD_JOBS' >&2; exit 1; }

download() {
  local name=$1 url=$2 digest=$3 archive="$PACKAGE/sources/$1"
  if [[ -n "${PREVIEW_SOURCE_DIRECTORY:-}" ]]; then
    cp "$PREVIEW_SOURCE_DIRECTORY/$name" "$archive"
  else
    curl --fail --location --retry 3 --output "$archive" "$url"
  fi
  [[ "$(shasum -a 256 "$archive" | cut -d ' ' -f 1)" == "$digest" ]] || { echo "Checksum mismatch: $name" >&2; exit 1; }
  printf '%s  %s\n' "$digest" "$name" >> "$PACKAGE/sources/SHA256SUMS"
  tar -xf "$archive" -C "$BUILD_DIRECTORY"
}
download libwebp-1.6.0.tar.gz https://storage.googleapis.com/downloads.webmproject.org/releases/webp/libwebp-1.6.0.tar.gz e4ab7009bf0629fd11982d4c2aa83964cf244cffba7347ecd39019a9e38c4564
download ffmpeg-8.0.tar.xz https://ffmpeg.org/releases/ffmpeg-8.0.tar.xz b2751fccb6cc4c77708113cd78b561059b6fa904b24162fa0be2d60273d27b8e
download zlib-1.3.1.tar.gz https://github.com/madler/zlib/releases/download/v1.3.1/zlib-1.3.1.tar.gz 9a93b2b7dfdac77ceba5a558a580e74667dd6fede4585b91eefb60f03b72df23
download jpegsrc.v9f.tar.gz https://www.ijg.org/files/jpegsrc.v9f.tar.gz 04705c110cb2469caa79fb71fba3d7bf834914706e9641a4589485c1f832565b
download LibRaw-0.22.2.tar.gz https://www.libraw.org/data/LibRaw-0.22.2.tar.gz de86b035655accff8d4010f1a221fdf50d353cb7b1422ba26f14a0db92612cfa
node "$REPOSITORY/previewworker/build/native-notices.mjs" "$BUILD_DIRECTORY" "$PACKAGE/licenses"

# Disable optional system dependency discovery; every non-system library is private.
cd "$BUILD_DIRECTORY/libwebp-1.6.0"
CC="$NATIVE_CC" CXX="$NATIVE_CXX" ./configure ${WEBP_TARGET[@]+"${WEBP_TARGET[@]}"} --prefix="$PREFIX" --enable-shared --disable-static \
  --disable-gl --disable-sdl --disable-png --disable-jpeg --disable-tiff --disable-gif \
  --disable-libwebpdemux --disable-libwebpmux
make -j "$JOBS"
make install
cp COPYING "$PACKAGE/licenses/libwebp-COPYING"
cp PATENTS "$PACKAGE/licenses/libwebp-PATENTS"
export PKG_CONFIG_PATH="$PREFIX/lib/pkgconfig"
export PKG_CONFIG_LIBDIR="$PREFIX/lib/pkgconfig"
cd "$BUILD_DIRECTORY/zlib-1.3.1"
if [[ "$GOOS" == linux && "$TARGET" != "$HOST_TARGET" ]]; then
  CC="$NATIVE_CC" CXX="$NATIVE_CXX" LDSHARED="$NATIVE_CC -shared -Wl,-soname,libz.so.1,--version-script,zlib.map,--undefined-version" ./configure --prefix="$PREFIX" --shared
else
  CC="$NATIVE_CC" CXX="$NATIVE_CXX" ./configure --prefix="$PREFIX" --shared
fi
make -j "$JOBS"
make install
cp LICENSE "$PACKAGE/licenses/zlib-LICENSE"
cd "$BUILD_DIRECTORY/jpeg-9f"
CC="$NATIVE_CC" CXX="$NATIVE_CXX" ./configure ${WEBP_TARGET[@]+"${WEBP_TARGET[@]}"} --prefix="$PREFIX" --enable-shared --disable-static
make -j "$JOBS"
make install
cp README "$PACKAGE/licenses/libjpeg-README"
cd "$BUILD_DIRECTORY/LibRaw-0.22.2"
RAW_LDFLAGS="-L$PREFIX/lib"
RAW_LIBS=''
if [[ "$GOOS" == linux ]]; then
  RAW_LDFLAGS+=' -Wl,-z,defs'
  if [[ "$CXX" == 'zig '* ]]; then
    # Libtool adds -nostdlib but cannot discover Zig's implicit C++ runtime.
    # Name Zig's bundled runtime explicitly and reject unresolved shared-library symbols.
    RAW_LIBS='-lc++ -lc++abi -lc'
  fi
fi
CPPFLAGS="-I$PREFIX/include" LDFLAGS="$RAW_LDFLAGS" LIBS="$RAW_LIBS" CC="$NATIVE_CC" CXX="$NATIVE_CXX" ./configure ${WEBP_TARGET[@]+"${WEBP_TARGET[@]}"} \
  --prefix="$PREFIX" --enable-shared --disable-static --disable-examples --disable-openmp \
  --disable-lcms --enable-jpeg --enable-zlib
make -j "$JOBS"
make install
cp COPYRIGHT LICENSE.LGPL LICENSE.CDDL "$PACKAGE/licenses/"
cd "$BUILD_DIRECTORY/ffmpeg-8.0"
# Some cross sysroots expose the legacy symbol without its removed header.
patch -p1 < "$REPOSITORY/previewworker/build/ffmpeg-sysctl-header.patch"
./configure ${FFMPEG_TARGET[@]+"${FFMPEG_TARGET[@]}"} --prefix="$PREFIX" --cc="$NATIVE_CC" --cxx="$NATIVE_CXX" \
  --extra-cflags="$CFLAGS" --extra-cxxflags="$CXXFLAGS" \
  --disable-autodetect --disable-programs --disable-doc --disable-debug \
  --disable-x86asm --disable-static --enable-shared --enable-pic --enable-libwebp --enable-zlib \
  --disable-network --disable-indevs --disable-outdevs \
  --disable-encoders --enable-encoder=libwebp --disable-muxers --disable-filters
# Public runtime metadata must be set before any library compiles; config.mak still owns real build paths.
node "$REPOSITORY/build/release/sanitize-native.mjs" header config.h "$BUILD_DIRECTORY"
make -j "$JOBS"
make install
cp COPYING.LGPLv2.1 LICENSE.md "$PACKAGE/licenses/"
node "$REPOSITORY/build/release/sanitize-native.mjs" copy ffbuild/config.mak "$PACKAGE/sources/ffmpeg-config.mak" "$BUILD_DIRECTORY"

# go-astiav links avdevice even though this worker never opens devices.
# Keep that ABI library while disabling its device implementations.
cd "$REPOSITORY/previewworker"
if [[ "$GOOS" == linux ]]; then
  export CGO_LDFLAGS="-L$PREFIX/lib "'-Wl,-rpath,$ORIGIN/preview-lib'
else
  export CGO_LDFLAGS="-L$PREFIX/lib -Wl,-rpath,@executable_path/preview-lib"
fi
go build -trimpath -ldflags "-s -w -X main.version=$RELEASE_VERSION" -o "$PACKAGE/yatm-preview" .
if [[ "$RUNNABLE" == true ]]; then
  LD_LIBRARY_PATH="$PREFIX/lib" YATM_TEST_HELPER="$PACKAGE/yatm-preview" go test -race -v ./...
  go vet ./...
else
  go test -c -o "$BUILD_DIRECTORY/previewworker.test" .
fi

if [[ "$GOOS" == linux ]]; then
  cp -RL "$PREFIX"/lib/*.so* "$PACKAGE/preview-lib/"
  for library in "$PACKAGE"/preview-lib/*.so*; do
    [[ ! -L "$library" ]] || continue
    patchelf --set-rpath '$ORIGIN' "$library"
    # Zig's bundled C++ runtime can retain debugger paths despite the source flags.
    if [[ "$TARGET" != "$HOST_TARGET" ]]; then
      llvm-objcopy --strip-debug "$library"
    else
      strip --strip-debug "$library"
    fi
  done
else
  cp -RL "$PREFIX"/lib/*.dylib "$PACKAGE/preview-lib/"
  for library in "$PACKAGE/yatm-preview" "$PACKAGE"/preview-lib/*.dylib; do
    [[ ! -L "$library" ]] || continue
    while IFS= read -r dependency; do
      if [[ "$dependency" == /* && -f "$PACKAGE/preview-lib/$(basename "$dependency")" ]]; then
        install_name_tool -change "$dependency" "@rpath/$(basename "$dependency")" "$library"
      fi
    done < <(otool -L "$library" | awk 'NR > 1 { print $1 }')
    if [[ "$library" == *.dylib ]]; then
      install_name_tool -id "@rpath/$(basename "$library")" "$library"
    fi
    codesign --force --sign - "$library"
  done
fi
if [[ "$RUNNABLE" == true ]]; then
  mv "$PREFIX" "$BUILD_DIRECTORY/native-build-only"
  "$PACKAGE/yatm-preview" --capabilities > "$PACKAGE/CAPABILITIES.json"
  printf '%s\n' "Helper tests and packaged capability discovery passed for $TARGET on $HOST_TARGET." > "$PACKAGE/VALIDATION"
else
  printf '%s\n' 'Cross compiled; helper tests compiled but runtime and capability discovery require target-host validation.' > "$PACKAGE/VALIDATION"
fi
cp "$REPOSITORY/LICENSE" "$PACKAGE/licenses/YATM-LICENSE"
cp "$(go env GOROOT)/LICENSE" "$PACKAGE/licenses/Go-LICENSE"

# Native library sources and the complete helper inputs permit an offline native rebuild.
node "$REPOSITORY/build/release/committed-inputs.mjs" preview "$REPOSITORY" "$PACKAGE/sources/yatm"
if [[ ! -d vendor ]]; then
  go mod vendor -o "$PACKAGE/sources/yatm/previewworker/vendor"
fi
node "$REPOSITORY/previewworker/build/package-sources.mjs" "$PACKAGE" "$RELEASE_COMMIT"
if [[ "$DISTRIBUTED_SOURCES" == true ]]; then
  # Preserve full dependency sources when the build uses only their vendored subset.
  rm -rf -- "$PACKAGE/sources/modules"
  cp -R "$PREVIEW_SOURCE_DIRECTORY/modules" "$PACKAGE/sources/"
fi
printf '%s\n' "$RELEASE_COMMIT" > "$PACKAGE/sources/COMMIT"
node "$REPOSITORY/build/release/committed-inputs.mjs" checksums "$PACKAGE/sources"
node "$REPOSITORY/build/release/committed-inputs.mjs" verify "$PACKAGE/sources" "$RELEASE_COMMIT"
printf '%s\n' "$RELEASE_VERSION" > "$PACKAGE/VERSION"
printf '%s\n' "$RELEASE_COMMIT" > "$PACKAGE/COMMIT"
mkdir -p "$RELEASE_DIRECTORY"
SOURCE_ARCHIVE="$RELEASE_DIRECTORY/yatm-preview-source-$GOOS-$GOARCH-$RELEASE_VERSION.tar.gz"
COPYFILE_DISABLE=1 tar --no-xattrs --no-acls -czf "$SOURCE_ARCHIVE" -C "$PACKAGE/sources" .
mv "$PACKAGE/sources" "$BUILD_DIRECTORY/distributed-sources"
cd "$RELEASE_DIRECTORY"
shasum -a 256 "$(basename "$SOURCE_ARCHIVE")" > "$SOURCE_ARCHIVE.sha256"
printf 'Corresponding source archive: %s\nSHA-256: %s\nDownload: https://github.com/samuelncui/yatm/releases/download/%s/%s\n' \
  "$(basename "$SOURCE_ARCHIVE")" "$(shasum -a 256 "$SOURCE_ARCHIVE" | cut -d ' ' -f 1)" "$RELEASE_VERSION" "$(basename "$SOURCE_ARCHIVE")" > "$PACKAGE/SOURCE.txt"
mkdir -p "$PACKAGE/preview-support"
for item in VERSION COMMIT licenses SOURCE.txt README.md VALIDATION CAPABILITIES.json; do
  [[ ! -e "$PACKAGE/$item" ]] || mv "$PACKAGE/$item" "$PACKAGE/preview-support/"
done
ARCHIVE="$RELEASE_DIRECTORY/yatm-preview-$GOOS-$GOARCH-$RELEASE_VERSION.tar.gz"
COPYFILE_DISABLE=1 tar --no-xattrs --no-acls -czf "$ARCHIVE" -C "$PACKAGE" .
cd "$RELEASE_DIRECTORY"
shasum -a 256 "$(basename "$ARCHIVE")" > "$ARCHIVE.sha256"
echo "Built $ARCHIVE"
