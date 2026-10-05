package main

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/internal/previewprotocol"
)

func fixtureFFmpeg(t *testing.T) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		if os.Getenv("YATM_TEST_HELPER") != "" {
			t.Fatal("ffmpeg fixture generator is required for packaged Preview acceptance")
		}
		t.Skip("ffmpeg fixture generator unavailable")
	}
	return ffmpeg
}

func videoFixture(t *testing.T) string {
	t.Helper()
	// Use fixed six-second GOPs so nearby requests must deduplicate real keyframes.
	ffmpeg := fixtureFFmpeg(t)
	path := filepath.Join(t.TempDir(), "source.mp4")
	output, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i",
		"testsrc2=size=128x96:rate=10", "-t", "12", "-c:v", "libx264", "-g", "60", "-keyint_min", "60",
		"-sc_threshold", "0", "-bf", "2", path).CombinedOutput()
	if err != nil {
		t.Fatalf("generate fixture: %v: %s", err, output)
	}
	return path
}

func videoRequest(source, output string) previewprotocol.Request {
	return previewprotocol.Request{Protocol: 1, Kind: "video", SourcePath: source, OutputDir: output,
		Format: "png", Quality: 75, PosterWidth: 128, PosterHeight: 128,
		TimelineWidth: 45, TimelineHeight: 81, TimelineIntervalSeconds: 1,
		TimelineMaxFrames: 12, MaxInputPixels: 64_000_000}
}

func TestSparseSeekRetainsActualKeyframeTimes(t *testing.T) {
	// Open one source context and sample targets within and across two GOPs.
	m, err := openMedia(context.Background(), videoFixture(t), true, 64_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	for _, test := range []struct{ target, want float64 }{{0, 0}, {1, 0}, {5, 0}, {6, 6}, {10, 6}, {11.9, 6}} {
		got, err := m.sample(context.Background(), test.target, true)
		if err != nil {
			t.Fatalf("sample %.2f: %v", test.target, err)
		}
		if got != test.want {
			t.Errorf("sample %.2f got %.3f want %.3f", test.target, got, test.want)
		}
	}
}

func TestVideoDedupOddDimensionsAndMapping(t *testing.T) {
	// Generate every encoding from the same source and verify compact, actual-time mapping.
	source := videoFixture(t)
	for _, format := range capabilities().OutputFormats {
		t.Run(format, func(t *testing.T) {
			request := videoRequest(source, t.TempDir())
			request.Format = format
			assets, err := generate(context.Background(), request, func(string, int, int) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if len(assets) != 3 || assets[1].Width != 90 || assets[1].Height != 81 {
				t.Fatalf("bad assets: %+v", assets)
			}
			mapping, err := os.ReadFile(filepath.Join(request.OutputDir, "timeline.vtt"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"00:00:00.000 --> 00:00:06.000", "00:00:06.000 --> 00:00:12.000", "#xywh=45,0,45,81"} {
				if !strings.Contains(string(mapping), want) {
					t.Errorf("missing %q in %s", want, mapping)
				}
			}
			if format == "png" {
				file, err := os.Open(filepath.Join(request.OutputDir, assets[1].Name))
				if err != nil {
					t.Fatal(err)
				}
				defer file.Close()
				img, err := png.Decode(file)
				if err != nil {
					t.Fatal(err)
				}
				if img.Bounds().Dx() != 90 || img.Bounds().Dy() != 81 {
					t.Fatalf("bad actual dimensions %v", img.Bounds())
				}
			}

			// Decode each actual encoded format through the linked native decoder.
			decoded, err := openMedia(context.Background(), filepath.Join(request.OutputDir, assets[1].Name), false, 64_000_000)
			if err != nil {
				t.Fatal(err)
			}
			defer decoded.close()
			if _, err := decoded.sample(context.Background(), 0, false); err != nil {
				t.Fatal(err)
			}
			if decoded.frame.Width() != 90 || decoded.frame.Height() != 81 {
				t.Fatal("encoded sprite dimensions mismatch")
			}
		})
	}
}

func TestWebPTransparency(t *testing.T) {
	// Test the optional linked encoder when the candidate build actually contains it.
	available := false
	for _, format := range capabilities().OutputFormats {
		available = available || format == "webp"
	}
	if !available {
		t.Skip("linked FFmpeg has no libwebp encoder")
	}
	img := image.NewNRGBA(image.Rect(0, 0, 45, 81))
	img.SetNRGBA(20, 20, color.NRGBA{R: 255, A: 255})
	directory := t.TempDir()
	asset, err := saveImage(directory, "thumbnail", "webp", 80, img)
	if err != nil {
		t.Fatal(err)
	}

	// Verify alpha survived native encoding instead of merely trusting its descriptor.
	decoded, err := openMedia(context.Background(), filepath.Join(directory, asset.Name), false, 64_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer decoded.close()
	if _, err := decoded.sample(context.Background(), 0, false); err != nil {
		t.Fatal(err)
	}
	result, err := decoded.render(45, 81, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.NRGBAAt(0, 0).A != 0 || result.NRGBAAt(20, 20).A != 255 {
		t.Fatal("WebP alpha was lost")
	}
}

func TestWebPEncodingIsDeterministic(t *testing.T) {
	available := false
	for _, format := range capabilities().OutputFormats {
		available = available || format == "webp"
	}
	if !available {
		t.Skip("linked FFmpeg has no libwebp encoder")
	}
	for _, alpha := range []bool{false, true} {
		img := image.NewNRGBA(image.Rect(0, 0, 161, 91))
		for y := 0; y < 91; y++ {
			for x := 0; x < 161; x++ {
				opacity := uint8(255)
				if alpha {
					opacity = uint8((x + y) % 256)
				}
				img.SetNRGBA(x, y, color.NRGBA{R: uint8(x), G: uint8(y), B: uint8(x ^ y), A: opacity})
			}
		}
		var expected []byte
		for attempt := 0; attempt < 5; attempt++ {
			var output bytes.Buffer
			if err := encodeWebP(&output, img, 80); err != nil {
				t.Fatal(err)
			}
			if attempt > 0 && !bytes.Equal(expected, output.Bytes()) {
				t.Fatalf("WebP changed on attempt %d (alpha=%v)", attempt, alpha)
			}
			expected = append(expected[:0], output.Bytes()...)
		}
		filename := filepath.Join(t.TempDir(), "encoded.webp")
		if err := os.WriteFile(filename, expected, 0o600); err != nil {
			t.Fatal(err)
		}
		decoded, err := openMedia(context.Background(), filename, false, 64_000_000)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decoded.sample(context.Background(), 0, false); err != nil {
			decoded.close()
			t.Fatal(err)
		}
		actual, err := decoded.render(161, 91, false)
		decoded.close()
		if err != nil {
			t.Fatal(err)
		}
		var total, channels int
		for y := 0; y < 91; y++ {
			for x := 0; x < 161; x++ {
				want, got := img.NRGBAAt(x, y), actual.NRGBAAt(x, y)
				if got.A != want.A {
					t.Fatalf("alpha mismatch at %d,%d: %d vs %d", x, y, got.A, want.A)
				}
				if want.A == 0 {
					continue
				}
				for _, difference := range []int{int(want.R) - int(got.R), int(want.G) - int(got.G), int(want.B) - int(got.B)} {
					if difference < 0 {
						difference = -difference
					}
					total += difference
					channels++
				}
			}
		}
		if float64(total)/float64(channels) > 12 {
			t.Fatalf("WebP color error too large: %f", float64(total)/float64(channels))
		}
	}
}

func TestRepeatedHelperGenerationIsDeterministic(t *testing.T) {
	helper := os.Getenv("YATM_TEST_HELPER")
	if helper == "" {
		t.Skip("set YATM_TEST_HELPER to test independent packaged worker processes")
	}
	for _, fixture := range []struct {
		name string
		args []string
	}{
		{name: "h264", args: []string{"-c:v", "libx264", "-preset", "ultrafast", "-g", "3", "-threads", "2"}},
		{name: "hevc10", args: []string{"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-preset", "ultrafast", "-x265-params", "pools=1:frame-threads=1:keyint=3:min-keyint=3:scenecut=0"}},
	} {
		source := filepath.Join(t.TempDir(), fixture.name+".mkv")
		args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=3", "-t", "3"}
		args = append(args, fixture.args...)
		output, err := exec.Command("ffmpeg", append(args, source)...).CombinedOutput()
		if err != nil {
			t.Fatalf("create %s: %v: %s", fixture.name, err, output)
		}
		for _, format := range []string{"png", "webp"} {
			t.Run(fixture.name+"/"+format, func(t *testing.T) {
				var previous [][]byte
				for attempt := 0; attempt < 5; attempt++ {
					request := videoRequest(source, t.TempDir())
					request.Format = format
					request.PosterWidth, request.PosterHeight = 512, 512
					request.TimelineWidth, request.TimelineHeight = 160, 90
					data, err := json.Marshal(request)
					if err != nil {
						t.Fatal(err)
					}
					command := exec.Command(helper)
					command.Stdin = bytes.NewReader(append(data, '\n'))
					output, err := command.CombinedOutput()
					if err != nil {
						t.Fatalf("helper: %v: %s", err, output)
					}
					var current [][]byte
					for index, name := range []string{"poster." + format, "timeline." + format, "timeline.vtt"} {
						content, err := os.ReadFile(filepath.Join(request.OutputDir, name))
						if err != nil {
							t.Fatal(err)
						}
						if attempt > 0 && !bytes.Equal(content, previous[index]) {
							t.Fatalf("%s differs on run %d: %d versus %d bytes", name, attempt, len(content), len(previous[index]))
						}
						current = append(current, content)
					}
					previous = current
				}
			})
		}
	}
}

func TestWorkerProtocolAndCancellation(t *testing.T) {
	// An incompatible request returns a single explicit error rather than ambiguous stdout.
	var output bytes.Buffer
	if err := run(context.Background(), nil, strings.NewReader("{\"protocol\":99}\n"), &output); err == nil {
		t.Fatal("invalid protocol accepted")
	}
	var event previewprotocol.Event
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "error" || event.Protocol != 1 {
		t.Fatalf("bad error event %+v", event)
	}

	// Cancellation stops native reads and removes owned outputs instead of publishing them.
	request := videoRequest(videoFixture(t), t.TempDir())
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output.Reset()
	if err := run(ctx, nil, bytes.NewReader(append(data, '\n')), &output); err == nil {
		t.Fatal("canceled request succeeded")
	}
	entries, err := os.ReadDir(request.OutputDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cancellation left outputs: %v %v", entries, err)
	}
}

func TestImageAndPixelLimit(t *testing.T) {
	// Decode a real image through the native path, not the old executable integration.
	ffmpeg := fixtureFFmpeg(t)
	source := filepath.Join(t.TempDir(), "图 像.png")
	output, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=red:s=160x120", "-frames:v", "1", source).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture: %v %s", err, output)
	}
	request := previewprotocol.Request{Protocol: 1, Kind: "image", SourcePath: source, OutputDir: t.TempDir(),
		Format: "png", Quality: 80, MaxWidth: 80, MaxHeight: 80, MaxInputPixels: 64_000_000}
	assets, err := generate(context.Background(), request, func(string, int, int) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].Width != 80 || assets[0].Height != 60 {
		t.Fatalf("bad thumbnail: %+v", assets)
	}

	// The decoder rejects oversized input, even when requested output is tiny.
	request.OutputDir = t.TempDir()
	request.MaxInputPixels = 100
	if _, err := generate(context.Background(), request, func(string, int, int) error { return nil }); err == nil {
		t.Fatal("pixel limit ignored")
	}
}

func TestNonZeroStartAndRotation(t *testing.T) {
	// Offset a real container and give its non-square pixels a rotated display without changing its GOPs.
	source := videoFixture(t)
	shifted := filepath.Join(t.TempDir(), "rotated.mov")
	output, err := exec.Command("ffmpeg", "-v", "error", "-itsoffset", "5", "-display_rotation", "90", "-i", source,
		"-c", "copy", "-aspect", "8:3", shifted).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture transform: %v %s", err, output)
	}
	m, err := openMedia(context.Background(), shifted, true, 64_000_000)
	if err != nil {
		t.Fatal(err)
	}
	defer m.close()
	if m.start == 0 {
		t.Fatal("fixture did not retain non-zero start")
	}

	// Sparse samples retain normalized timestamps while display dimensions apply both SAR and rotation.
	actual, err := m.sample(context.Background(), 6, true)
	if err != nil {
		t.Fatal(err)
	}
	if actual != 6 {
		t.Fatalf("timestamp was not normalized: %.3f", actual)
	}
	img, err := m.render(128, 128, false)
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != 48 || img.Bounds().Dy() != 128 {
		t.Fatalf("sample aspect ratio and rotation not applied: %v", img.Bounds())
	}
}

func TestProtocolRejectsInvalidLimitsAndPreservesExistingFiles(t *testing.T) {
	// Reject unsafe output and budgets before touching caller-owned data.
	source := videoFixture(t)
	request := videoRequest(source, t.TempDir())
	marker := filepath.Join(request.OutputDir, "poster.png")
	if err := os.WriteFile(marker, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := run(context.Background(), nil, bytes.NewReader(data), &output); err == nil {
		t.Fatal("non-empty output accepted")
	}
	contents, err := os.ReadFile(marker)
	if err != nil || string(contents) != "existing" {
		t.Fatalf("existing file changed: %q %v", contents, err)
	}
	request.OutputDir = t.TempDir()
	request.TimelineMaxFrames = 1001
	if err := validate(request); err == nil {
		t.Fatal("unbounded frame count accepted")
	}
}

func TestMatroskaNonZeroStart(t *testing.T) {
	// Matroska headers can omit stream start_time while storing a duration endpoint.
	source := videoFixture(t)
	shifted := filepath.Join(t.TempDir(), "offset.mkv")
	output, err := exec.Command("ffmpeg", "-v", "error", "-itsoffset", "5", "-i", source, "-c", "copy", shifted).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture transform: %v %s", err, output)
	}
	request := videoRequest(shifted, t.TempDir())
	assets, err := generate(context.Background(), request, func(string, int, int) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if assets[1].Width != 90 {
		t.Fatalf("expected two distinct keyframes, got %+v", assets)
	}
	mapping, err := os.ReadFile(filepath.Join(request.OutputDir, "timeline.vtt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mapping), "00:00:06.000 --> 00:00:12.000") {
		t.Fatalf("incorrect normalized duration: %s", mapping)
	}
}
