// yatm-preview is a one-file, optional native media worker. Stdout is exclusively JSONL.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/asticode/go-astiav"
	"github.com/samuelncui/yatm/internal/previewprotocol"
)

const (
	maxDimension    = 8192
	maxFrames       = 1000
	maxSpritePixels = 100_000_000
	maxAssetBytes   = 128 << 20
	maxAssetsBytes  = 256 << 20
)

var version = "dev"

func main() {
	// Keep native diagnostics separate from the bounded protocol stream.
	astiav.SetLogLevel(astiav.LogLevelError)
	limitNativeAllocation()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	// Capabilities is an independent request that never opens a media file.
	encoder := json.NewEncoder(output)
	emit := func(event previewprotocol.Event) error {
		event.Protocol = previewprotocol.Version
		if len(event.Error) > 4096 {
			event.Error = event.Error[:4096] + " (truncated)"
		}
		return encoder.Encode(event)
	}
	if len(args) == 1 && args[0] == "--capabilities" {
		capabilities := capabilities()
		return emit(previewprotocol.Event{Type: "capabilities", Capabilities: &capabilities})
	}
	if len(args) != 0 {
		return fmt.Errorf("usage: yatm-preview [--capabilities]")
	}

	// Read exactly one bounded line, allowing a caller to keep stdin open while waiting.
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var request previewprotocol.Request
	err := error(nil)
	if !scanner.Scan() {
		err = scanner.Err()
		if err == nil {
			err = fmt.Errorf("missing request")
		}
	} else {
		decoder := json.NewDecoder(strings.NewReader(scanner.Text()))
		decoder.DisallowUnknownFields()
		err = decoder.Decode(&request)
		if err == nil {
			var trailing any
			if decoder.Decode(&trailing) != io.EOF {
				err = fmt.Errorf("multiple request values")
			}
		}
	}
	if err == nil {
		err = validate(request)
	}
	if err != nil {
		_ = emit(previewprotocol.Event{Type: "error", Error: err.Error()})
		return err
	}

	// Publish only a complete result; cleanup is restricted to worker-owned output names.
	started := time.Now()
	assets, err := generate(ctx, request, func(phase string, completed, total int) error {
		return emit(previewprotocol.Event{Type: "progress", Phase: phase, Completed: completed,
			Total: total, ElapsedMS: time.Since(started).Milliseconds()})
	})
	if err != nil {
		cleanup(request.OutputDir)
		_ = emit(previewprotocol.Event{Type: "error", Error: err.Error(), ElapsedMS: time.Since(started).Milliseconds()})
		return err
	}
	return emit(previewprotocol.Event{Type: "result", Assets: assets, ElapsedMS: time.Since(started).Milliseconds()})
}

func validate(request previewprotocol.Request) error {
	// Validate protocol, codec selection and bounded image geometry before native allocation.
	if request.Protocol != previewprotocol.Version {
		return fmt.Errorf("unsupported protocol %d", request.Protocol)
	}
	if request.Kind != "image" && request.Kind != "video" {
		return fmt.Errorf("unsupported kind %q", request.Kind)
	}
	if !slices.Contains(capabilities().OutputFormats, request.Format) {
		return fmt.Errorf("unsupported output format %q", request.Format)
	}
	if request.Quality < 1 || request.Quality > 100 {
		return fmt.Errorf("quality must be between 1 and 100")
	}
	if request.MaxInputPixels <= 0 || request.MaxInputPixels > 64_000_000 {
		return fmt.Errorf("input pixel limit must be between 1 and 64000000")
	}
	dimensions := []int{request.MaxWidth, request.MaxHeight}
	if request.Kind == "video" {
		dimensions = []int{request.PosterWidth, request.PosterHeight, request.TimelineWidth, request.TimelineHeight}
		if request.TimelineIntervalSeconds <= 0 || request.TimelineMaxFrames <= 0 || request.TimelineMaxFrames > maxFrames {
			return fmt.Errorf("invalid timeline sampling")
		}
		columns := min(request.TimelineMaxFrames, 10)
		rows := (request.TimelineMaxFrames + columns - 1) / columns
		if int64(columns)*int64(rows)*int64(request.TimelineWidth)*int64(request.TimelineHeight) > maxSpritePixels {
			return fmt.Errorf("timeline exceeds pixel limit")
		}
	}
	for _, dimension := range dimensions {
		if dimension <= 0 || dimension > maxDimension {
			return fmt.Errorf("dimension must be between 1 and %d", maxDimension)
		}
	}

	// Accept only explicit local files and an empty existing output directory.
	if !filepath.IsAbs(request.SourcePath) || !filepath.IsAbs(request.OutputDir) {
		return fmt.Errorf("source and output paths must be absolute")
	}
	info, err := os.Lstat(request.SourcePath)
	if err != nil {
		return fmt.Errorf("inspect source failed: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file")
	}
	info, err = os.Lstat(request.OutputDir)
	if err != nil {
		return fmt.Errorf("inspect output directory failed: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("output is not a directory")
	}
	entries, err := os.ReadDir(request.OutputDir)
	if err != nil {
		return fmt.Errorf("read output directory failed: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("output directory must be empty")
	}
	return nil
}

func capabilities() previewprotocol.Capabilities {
	// Container extensions are hints; the actual codec is checked when opening the source.
	result := previewprotocol.Capabilities{Version: version, FFmpegVersion: nativeVersion(), LibRawVersion: rawVersion(),
		InputExtensionsByKind: map[string][]string{"image": append([]string(nil), rawExtensions...)},
		InputExtensions:       append([]string(nil), rawExtensions...),
		Kinds:                 []string{"image", "video"}, OutputFormats: []string{"png", "jpeg"}}
	if astiav.FindEncoderByName("libwebp") != nil {
		result.OutputFormats = append(result.OutputFormats, "webp")
	}
	for _, route := range []struct {
		decoder    string
		extensions []string
	}{
		{"mjpeg", []string{"jpg", "jpeg"}}, {"png", []string{"png"}}, {"webp", []string{"webp"}},
		{"gif", []string{"gif"}}, {"bmp", []string{"bmp"}}, {"tiff", []string{"tif", "tiff"}},
		{"hevc", []string{"heic", "heif"}}, {"av1", []string{"avif"}},
		{"h264", []string{"mp4", "mkv", "mov", "m4v", "ts", "mts", "m2ts"}},
		{"mpeg4", []string{"avi"}}, {"vp9", []string{"webm"}}, {"mpeg2video", []string{"mpg", "mpeg", "vob"}},
	} {
		if astiav.FindDecoderByName(route.decoder) != nil {
			result.InputExtensions = append(result.InputExtensions, route.extensions...)
			kind := "image"
			switch route.decoder {
			case "h264", "mpeg4", "vp9", "mpeg2video":
				kind = "video"
			}
			result.InputExtensionsByKind[kind] = append(result.InputExtensionsByKind[kind], route.extensions...)
		}
	}
	return result
}

func cleanup(directory string) {
	for _, name := range []string{"thumbnail.png", "thumbnail.jpeg", "thumbnail.webp", "poster.png", "poster.jpeg",
		"poster.webp", "timeline.png", "timeline.jpeg", "timeline.webp", "timeline.vtt"} {
		_ = os.Remove(filepath.Join(directory, name))
	}
}
