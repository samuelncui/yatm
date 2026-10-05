# Native Preview Worker

`yatm-preview` is an optional, one-request process. The main YATM module imports only the standard-library protocol in `internal/previewprotocol`; native FFmpeg and LibRaw dependencies remain in this nested module.

## Build and Protocol

Build with CGO, FFmpeg 8 and LibRaw 0.22.2 development libraries available through `pkg-config`:

```sh
cd previewworker
CGO_ENABLED=1 go build -o /tmp/yatm-preview .
/tmp/yatm-preview --capabilities
```

The pinned `go-astiav v0.42.0` binding requires FFmpeg 8 ABI libraries. All seven libraries referenced by its pkg-config definition must exist, including `libavdevice`, `libavfilter` and `libswresample`. WebP output additionally requires the FFmpeg `libwebp` encoder. PNG and JPEG output use Go's encoders after native decoding/scaling. Actual output support is reported by `--capabilities`.

Normal execution reads one newline-terminated JSON request and writes JSONL progress followed by one `result` or `error`. The caller supplies an absolute local regular-file source and an empty, caller-owned output directory. The caller enforces its timeout, validates generated assets and source identity, and publishes the final bundle. An input error does not clean or alter pre-existing output files. Diagnostics are written to stderr.

## Sampling and Resource Bounds

One media/decoder context is reused for all ascending sparse seeks. Video decoding discards non-key frames; decoder buffers are flushed after each seek. Repeated keyframes occupy one sprite cell. WebVTT uses actual normalized sample times, with the first/last sample covering the beginning/end of the playback range. `NOTE sampled at` records the actual first-frame time independently of a cue's coverage interval.

The worker never falls back to decoding the complete film. A single opened file backs custom native I/O with a 64 KiB buffer. Opening/probing and each sample have a 64 MiB input-byte budget, including reads inside native seek operations; sample packet processing additionally stops after 4096 packets. The decoder uses two slice threads and a maximum of 64 million input pixels, including mid-stream dimension changes. Native single allocations are limited to 256 MiB. These controls are not a total RSS guarantee. Output retains the 8192-dimension, 1000-sample, 100-million-sprite-pixel, 128 MiB per-asset and 256 MiB combined limits.

Right-angle display rotation and sample aspect ratio are applied before output. Containers must describe their visual codec without `FindStreamInfo` decoding: implicit probe decoding would bypass the configured decoder pixel limit. A source requiring that probe, non-orthogonal rotation, missing duration, invalid timestamps, inaccessible sources and failed sparse positioning return explicit errors. The helper neither updates metadata nor computes content signatures.

## Camera RAW

Image routes include DNG, CR2, CR3, NEF, NRW, ARW, RAF, ORF, RW2, PEF and SRW. LibRaw recognizes the actual camera/container: an extension is not a guarantee for every camera or compression variant. Capability discovery reports the linked LibRaw version and input extensions grouped by type. The private JPEG library enables lossy DNG decoding; deflate DNG uses the private zlib. DNG SDK-only compression variants are not enabled.

The worker first tries the smallest embedded preview that satisfies the configured output without upscaling. Missing, damaged or undersized previews fall back to actual RAW development: camera white balance with automatic fallback, sRGB 8-bit output and half-size development when sufficient. LibRaw's eight orientation transformations are normalized after target-size resampling. Camera JPEG rendering and developed color need not be identical.

Source dimensions remain subject to the configured 64-million-pixel maximum, including RAW sources with embedded previews. LibRaw's working allocation limit is bounded by input pixels and capped at 512 MiB; processed buffers are checked before access, and the full developed bitmap is viewed rather than copied into Go memory. Embedded JPEG dimensions are checked before decode. Native cancellation callbacks complement the caller's per-file process timeout. These limits are independent safeguards, not an aggregate RSS guarantee across multiple helper processes.

## Local Tests

```sh
go test ./...
go test -race ./...
go vet ./...
```

Integration fixtures use the installed `ffmpeg` command only to create test inputs. Production generation does not invoke it. Tests cover sparse keyframe positions, deduplication, actual-time mapping, odd tile geometry, source pixel limits, cancellation, protocol validation, non-zero MP4/Matroska starts, rotation, and available output encoders.

RAW unit tests construct genuine uncompressed CFA DNG data with and without an embedded JPEG, including a damaged-thumbnail fallback. Real-camera acceptance uses external CC0 fixtures, never committed camera files:

```sh
node build/fetch-raw-fixtures.mjs /absolute/path/to/fixtures
YATM_TEST_RAW_DIRECTORY=/absolute/path/to/fixtures go test -run RAWRealCameraFixtures -v
```

The [fixture inventory](testdata/raw-fixtures.json) pins eleven [raw.pixls.us](https://raw.pixls.us/)
samples by URL, SHA-256 and CC0 license, covering every advertised RAW extension family and both
embedded Preview and development paths. The download tool verifies existing files and refuses a
checksum mismatch. Camera bytes total about 94 MB and remain outside the repository and packages;
release CI downloads them before running the native tests. Local tests without the external
directory skip only this real-camera acceptance.

That directory's `manifest.json` contains `camera`, absolute `path`, `sha256` and `license`
(`CC0-1.0`) records. The test verifies every checksum, requires all advertised RAW extension
families, checks output decode/geometry and logs the actual embedded/development path and elapsed
time. One sample per family does not establish support for every camera or compression variant.
