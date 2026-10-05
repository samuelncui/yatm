# Native Preview Helper Builds

The optional helper package is separate from the main YATM package. It contains
`yatm-preview`, private `preview-lib` shared libraries and `preview-support` with
version/commit identity, dependency notices and a corresponding-source manifest. Library aliases
are ordinary files; the binary archive requires no symlinks. Pinned native archives and vendored Go
inputs are distributed in the separate `yatm-preview-source-<os>-<arch>-<version>.tar.gz` asset with
its own checksum. Transfer only the binary package to acceptance hosts. It does not install or
replace system `ffmpeg` or `ffprobe`. The main programs never link FFmpeg; [backend
builds](../../docs/operations/testing.md#release-backend-builds) retain the static Linux C SQLite
implementation.

## Native Build

Supported build hosts are Linux amd64, macOS amd64 and macOS arm64. Use Go 1.26.8, Node 24, a native
C/C++ compiler, make, pkg-config, curl, tar and shasum; Linux additionally requires patchelf, and
macOS requires the Xcode command-line tools. The helper uses go-astiav v0.42.0, FFmpeg 8.0, libwebp
1.6.0, zlib 1.3.1, LibRaw 0.22.2 and IJG JPEG 9f. Native source archives are checksum-verified by
the [build script](build.sh). Optional system-library discovery, network protocols, capture devices
and FFmpeg command-line programs are disabled.

The included [configure patch](ffmpeg-sysctl-header.patch) checks the `sysctl` header as well as its
symbol; some cross sysroots retain the legacy symbol without that header. The corresponding-source
archive includes this patch and its build recipe. Dependency patches live with their consuming build
tooling and record their base version, reproduction steps and removal condition.

```shell
RELEASE_VERSION=<tag> ./previewworker/build/build.sh
```

The build uses a fresh temporary directory and never installs to a system prefix. Runnable targets
run helper tests with Go's race detector, vet and packaged capability discovery before creating the binary and corresponding-source archives
with SHA-256 companions in `output/releases/` by default. The package is relocatable; preserve the
sibling `preview-lib` directory. Native libraries are dynamically linked to permit replacement under
their license. FFmpeg uses LGPL components with no GPL/nonfree configure options. The packages carry
the required dependency licenses and exact source archives. LibRaw disables OpenMP, LCMS and example
programs; file concurrency is owned by the Preview manager.

Native packages retain the pinned libraries' complete root licenses, copyright and patent notices,
plus verbatim source notices for embedded dependencies such as LibRaw's DCB/FBDD and X3F and
FFmpeg's IJG-derived code. Zig builds retain the bundled runtime notices. The corresponding-source
archive includes the notice and fixture-preflight tools used by the build; package validation
requires their distributed notices and rejects unowned members.

Runnable builds first verify a separate FFmpeg fixture generator through
`node build/release/check-preview-fixtures.mjs`. It needs libx264, 10-bit libx265, PNG, lavfi and
display-rotation support. The [test guide](../../docs/operations/testing.md#release-sop) describes
the pinned Linux fixture-tool build. These test encoders are not added to the Preview runtime.

Repository builds require a clean HEAD: tracked edits and untracked nonignored files fail before
compilation. A supplied `RELEASE_COMMIT` must equal HEAD. Build and corresponding-source inputs are
extracted from that commit; ignored local files, cached vendor trees and private output do not enter
the package. Binary packages allow only the helper, its named private libraries and the support files
and notices owned by the dependency manifest.
Go module-cache sources are verified before copying, so local edits or extra files cannot enter the
corresponding-source archive. Distributed offline builds retain their verified source inventory.

Native release objects omit debugger paths and map compiler source locations to `/build/yatm-preview`.
FFmpeg's embedded configure metadata uses that public root before library compilation. The distributed
`ffmpeg-config.mak` is a sanitized copy made after compilation; build-used paths remain intact. These
changes preserve configure flags, LGPL choices and the private libraries' runtime identities.
Linux package libraries also discard debug sections after relocation, preserving exported symbols.

`PREVIEW_TARGET=linux-amd64` permits local cross compilation with Zig 0.15.2, host patchelf and LLVM objcopy. It
targets glibc 2.35 and packages the same private native libraries. Cross builds compile helper tests
but cannot run them or discover capabilities; `preview-support/VALIDATION` records that limitation.
Validate those exact bytes on the intended isolated Linux host before accepting them.

Linux LibRaw links reject unresolved symbols. The Zig cross build explicitly links Zig's bundled
libc++ and libc++abi into the private LibRaw library and includes their notices; it does not require
the target host's libstdc++.

On Apple Silicon, `PREVIEW_TARGET=darwin-amd64` builds the Intel macOS target using the Xcode
compiler. Rosetta is required to run helper tests and the relocated capability probe. macOS builds
default `MACOSX_DEPLOYMENT_TARGET` to `15.0`, matching the CI baseline. Release CI prepares the
checksum-pinned CC0 camera fixtures and sets `YATM_TEST_RAW_DIRECTORY` for every runnable native
target, as documented in the [worker tests](../README.md#local-tests). Local and offline builds may
provide the same external directory; the source package includes the inventory and download tool,
without bundling camera files or requiring a download during an offline rebuild.

Use the optional helper installation described in the [installation
guide](../../docs/operations/install.md#preview-preferences), or verify its checksum and extract it
in an explicitly selected installation. Configure the native helper executable path in Preview
Settings when it is outside YATM's executable directory.

## Native Compiler Cache

Set `CCACHE_DIR` to opt into [ccache](https://ccache.dev/manual/latest.html) when it is installed.
Without it, the build uses the ordinary compilers. CI restores the native object cache by target,
compiler and SDK identity, and native build recipe; a YATM source commit alone does not discard it.
Each build still creates the Go helper, verifies runtime behavior and collects licenses and
corresponding sources from its current committed inputs.

The cache normalizes the temporary build root with `CCACHE_BASEDIR`, retains the public compiler
path maps and checks compiler content. It does not relax header or source checks. Native configure
commands use the cache wrapper; Go and runtime-license discovery retain the underlying compiler.
CI reports cache statistics. Compare one cold and one warm build in the same environment when
changing this integration; verify hits, elapsed time, relocation and path sanitization. From a
repository checkout:

```shell
RELEASE_VERSION=<tag> node previewworker/build/check-cache.mjs /new/private/cache-check-output
```

This optional check runs the normal build twice with its own fresh cache, retaining logs and a
`report.json`. `PREVIEW_SOURCE_DIRECTORY` can supply verified native archives to exclude downloads
from the comparison. The check does not clear a user's existing cache or add another release gate.

## Rebuild From Distributed Sources

The corresponding-source archive includes the exact native archives, checksums, FFmpeg
configuration, Go modules with notices and a vendored helper source tree. Its `yatm/` subtree mirrors
`previewworker/build/` and includes the build script, patch and source-packaging tool with the shared
protocol and the provenance checks under `build/release/`. `COMMIT` records its source identity and
`SHA256SUMS` inventories every source file, including generated vendor and module sources. Extract it
locally and use an absolute source path:

```shell
cd yatm
GOPROXY=off PREVIEW_SOURCE_DIRECTORY=/absolute/path/to/extracted-source-archive \
  RELEASE_VERSION=<tag> RELEASE_COMMIT=<package-COMMIT> ./previewworker/build/build.sh
```

The source inputs are pinned; host compiler and SDK versions still affect output bytes. Linux helper
packages use the build host's glibc baseline; CI uses Ubuntu 22.04. macOS packages use macOS 15
runners. Static main-program portability does not imply the same baseline for this optional helper.

This rebuild requires no `.git`. Supply `RELEASE_COMMIT` explicitly; it must match the source
`COMMIT`, and every source file must match the inventory before compilation. Extra files, missing
files, changed bytes and private configuration fail validation. Keep the extracted source tree
unchanged. The default rebuild output is `output/releases/` beside the extracted source directory;
`RELEASE_DIRECTORY` can select another location outside it. Full module sources and pinned native
archives remain in the rebuilt corresponding-source package.
