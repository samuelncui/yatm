package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/samuelncui/yatm/internal/previewprotocol"
)

// rawFixture writes an actual uncompressed CFA DNG, not a renamed ordinary image.
// Pixels and metadata are synthetic test data authored for this repository.
func rawFixture(t *testing.T, thumbnail bool) string {
	t.Helper()
	// Assemble a minimal TIFF/DNG directory with a known Bayer mosaic and camera matrix.
	type tag struct {
		id, kind uint16
		count    uint32
		data     []byte
	}
	shorts := func(values ...uint16) []byte {
		b := new(bytes.Buffer)
		for _, v := range values {
			_ = binary.Write(b, binary.LittleEndian, v)
		}
		return b.Bytes()
	}
	longs := func(values ...uint32) []byte {
		b := new(bytes.Buffer)
		for _, v := range values {
			_ = binary.Write(b, binary.LittleEndian, v)
		}
		return b.Bytes()
	}
	const width, height = 128, 96
	pixels := make([]byte, width*height*2)
	for i := 0; i < width*height; i++ {
		binary.LittleEndian.PutUint16(pixels[i*2:], uint16(8000+(i%width)*200))
	}
	tags := []tag{
		{254, 4, 1, longs(0)}, {256, 4, 1, longs(width)}, {257, 4, 1, longs(height)},
		{258, 3, 1, shorts(16)}, {259, 3, 1, shorts(1)}, {262, 3, 1, shorts(32803)},
		{271, 2, 5, []byte("YATM\x00")}, {272, 2, 8, []byte("Fixture\x00")},
		{273, 4, 1, longs(0)}, {274, 3, 1, shorts(1)}, {277, 3, 1, shorts(1)},
		{278, 4, 1, longs(height)}, {279, 4, 1, longs(uint32(len(pixels)))},
		{284, 3, 1, shorts(1)}, {33421, 3, 2, shorts(2, 2)}, {33422, 1, 4, []byte{0, 1, 1, 2}},
		{50706, 1, 4, []byte{1, 4, 0, 0}}, {50707, 1, 4, []byte{1, 1, 0, 0}},
		{50708, 2, 13, []byte("YATM Fixture\x00")}, {50717, 4, 1, longs(65535)},
		{50721, 10, 9, longs(1, 1, 0, 1, 0, 1, 0, 1, 1, 1, 0, 1, 0, 1, 0, 1, 1, 1)},
		{50728, 5, 3, longs(1, 1, 1, 1, 1, 1)}, {50778, 3, 1, shorts(21)},
	}
	var embedded []byte
	if thumbnail {
		img := image.NewRGBA(image.Rect(0, 0, width/2, height/2))
		for y := 0; y < height/2; y++ {
			for x := 0; x < width/2; x++ {
				img.SetRGBA(x, y, color.RGBA{200, 30, 20, 255})
			}
		}
		buffer := new(bytes.Buffer)
		if err := jpeg.Encode(buffer, img, nil); err != nil {
			t.Fatal(err)
		}
		embedded = buffer.Bytes()
	}
	slices.SortFunc(tags, func(a, b tag) int { return int(a.id) - int(b.id) })

	// Resolve file offsets after laying out the IFD and its out-of-line values.
	data := make([]byte, 8+2+len(tags)*12+4)
	copy(data, []byte{'I', 'I', 42, 0, 8, 0, 0, 0})
	binary.LittleEndian.PutUint16(data[8:], uint16(len(tags)))
	for i, item := range tags {
		entry := data[10+i*12:]
		binary.LittleEndian.PutUint16(entry, item.id)
		binary.LittleEndian.PutUint16(entry[2:], item.kind)
		binary.LittleEndian.PutUint32(entry[4:], item.count)
		if len(item.data) <= 4 {
			copy(entry[8:12], item.data)
		} else {
			binary.LittleEndian.PutUint32(entry[8:], uint32(len(data)))
			data = append(data, item.data...)
			if len(data)%2 != 0 {
				data = append(data, 0)
			}
		}
	}
	for i, item := range tags {
		if item.id == 273 {
			binary.LittleEndian.PutUint32(data[10+i*12+8:], uint32(len(data)))
		}
	}
	data = append(data, pixels...)
	if thumbnail {
		thumbTags := []tag{{254, 4, 1, longs(1)}, {256, 4, 1, longs(width / 2)}, {257, 4, 1, longs(height / 2)},
			{259, 3, 1, shorts(6)}, {262, 3, 1, shorts(6)}, {274, 3, 1, shorts(1)},
			{513, 4, 1, longs(uint32(len(data) + 2 + 8*12 + 4))}, {514, 4, 1, longs(uint32(len(embedded)))}}
		binary.LittleEndian.PutUint32(data[10+len(tags)*12:], uint32(len(data)))
		thumbIFD := make([]byte, 2+len(thumbTags)*12+4)
		binary.LittleEndian.PutUint16(thumbIFD, uint16(len(thumbTags)))
		for i, item := range thumbTags {
			entry := thumbIFD[2+i*12:]
			binary.LittleEndian.PutUint16(entry, item.id)
			binary.LittleEndian.PutUint16(entry[2:], item.kind)
			binary.LittleEndian.PutUint32(entry[4:], item.count)
			copy(entry[8:12], item.data)
		}
		data = append(data, thumbIFD...)
		data = append(data, embedded...)
	}
	path := filepath.Join(t.TempDir(), "fixture.dng")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRAWDevelopmentAndEmbeddedPreview(t *testing.T) {
	for _, embedded := range []bool{false, true} {
		t.Run(map[bool]string{false: "develop", true: "embedded"}[embedded], func(t *testing.T) {
			// Decode genuine CFA data or extract the authored JPEG from a DNG container.
			request := previewprotocol.Request{Kind: "image", SourcePath: rawFixture(t, embedded), OutputDir: t.TempDir(), Format: "png", Quality: 80, MaxWidth: 32, MaxHeight: 32, MaxInputPixels: 64_000_000}
			var phases []string
			assets, err := generate(context.Background(), request, func(phase string, _, _ int) error { phases = append(phases, phase); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if len(assets) != 1 || assets[0].Width != 32 || assets[0].Height != 24 {
				t.Fatalf("unexpected assets: %+v", assets)
			}
			file, err := os.Open(filepath.Join(request.OutputDir, assets[0].Name))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := png.Decode(file)
			_ = file.Close()
			if err != nil {
				t.Fatal(err)
			}
			r, g, b, _ := decoded.At(16, 12).RGBA()
			if r+g+b == 0 {
				t.Fatal("RAW image rendered black")
			}
			if slices.Contains(phases, "developing") == embedded {
				t.Fatalf("wrong RAW path: embedded=%t phases=%v", embedded, phases)
			}
		})
	}
}

func TestRAWLimitsAndCancellation(t *testing.T) {
	// Reject the source before development and reject cancellation before opening it.
	request := previewprotocol.Request{Kind: "image", SourcePath: rawFixture(t, false), OutputDir: t.TempDir(), Format: "png", Quality: 80, MaxWidth: 32, MaxHeight: 32, MaxInputPixels: 100}
	if _, err := generate(context.Background(), request, func(string, int, int) error { return nil }); err == nil {
		t.Fatal("accepted over-limit RAW")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := generate(ctx, request, func(string, int, int) error { return nil }); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestRAWCorruptEmbeddedFallback(t *testing.T) {
	// Damage only the embedded JPEG, retaining valid CFA data for development.
	path := rawFixture(t, true)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.LastIndex(data, []byte{0xff, 0xd8, 0xff})
	if start < 0 {
		t.Fatal("missing fixture JPEG")
	}
	for i := start; i < len(data); i++ {
		data[i] = 0
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	request := previewprotocol.Request{Kind: "image", SourcePath: path, OutputDir: t.TempDir(), Format: "png", Quality: 80, MaxWidth: 32, MaxHeight: 32, MaxInputPixels: 64_000_000}

	// Thumbnail damage is not a source-content verification result and must not prevent valid RAW development.
	var phases []string
	assets, err := generate(context.Background(), request, func(phase string, _, _ int) error { phases = append(phases, phase); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || !slices.Contains(phases, "developing") {
		t.Fatalf("missing development fallback: assets=%v phases=%v", assets, phases)
	}
}

func TestRAWUndersizedEmbeddedFallback(t *testing.T) {
	// The fixture JPEG is only 64x48, while the requested derivative needs all 128x96 sensor pixels.
	request := previewprotocol.Request{Kind: "image", SourcePath: rawFixture(t, true), OutputDir: t.TempDir(), Format: "jpeg", Quality: 80, MaxWidth: 128, MaxHeight: 128, MaxInputPixels: 64_000_000}
	var phases []string
	assets, err := generate(context.Background(), request, func(phase string, _, _ int) error { phases = append(phases, phase); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(assets) != 1 || assets[0].Width != 128 || assets[0].Height != 96 || !slices.Contains(phases, "developing") {
		t.Fatalf("undersized thumbnail was used: assets=%v phases=%v", assets, phases)
	}
}

func TestRAWCancelBeforeDevelopment(t *testing.T) {
	// Cancel after metadata was opened so native setup and callback cleanup are exercised.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := previewprotocol.Request{Kind: "image", SourcePath: rawFixture(t, false), OutputDir: t.TempDir(), Format: "png", Quality: 80, MaxWidth: 32, MaxHeight: 32, MaxInputPixels: 64_000_000}
	_, err := generate(ctx, request, func(phase string, _, _ int) error {
		if phase == "developing" {
			cancel()
		}
		return nil
	})
	if err != context.Canceled {
		t.Fatalf("expected canceled generation, got %v", err)
	}
	entries, err := os.ReadDir(request.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("canceled RAW published an output")
	}
}

func TestRAWOrientation(t *testing.T) {
	// Label a rectangular source so a wrong mirror or rotation cannot preserve the expected arrangement.
	img := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: uint8(y*3 + x + 1), A: 255})
		}
	}

	// LibRaw's eight transforms must preserve exact geometry and pixel positions, not just membership.
	for flip, want := range []struct {
		width, height int
		pixels        [6]uint8
	}{
		{3, 2, [6]uint8{1, 2, 3, 4, 5, 6}},
		{3, 2, [6]uint8{3, 2, 1, 6, 5, 4}},
		{3, 2, [6]uint8{4, 5, 6, 1, 2, 3}},
		{3, 2, [6]uint8{6, 5, 4, 3, 2, 1}},
		{2, 3, [6]uint8{1, 4, 2, 5, 3, 6}},
		{2, 3, [6]uint8{3, 6, 2, 5, 1, 4}},
		{2, 3, [6]uint8{4, 1, 5, 2, 6, 3}},
		{2, 3, [6]uint8{6, 3, 5, 2, 4, 1}},
	} {
		got := orientRAW(img, flip)
		if got.Bounds() != image.Rect(0, 0, want.width, want.height) {
			t.Fatalf("orientation %d dimensions: %v", flip, got.Bounds())
		}
		for y := 0; y < want.height; y++ {
			for x := 0; x < want.width; x++ {
				if value := got.NRGBAAt(x, y).R; value != want.pixels[y*want.width+x] {
					t.Fatalf("orientation %d pixel %d,%d: got %d want %d", flip, x, y, value, want.pixels[y*want.width+x])
				}
			}
		}
	}
}

func TestRAWRealCameraFixtures(t *testing.T) {
	// Acceptance media stays outside the repository and must match its recorded licensed fixture manifest.
	directory := os.Getenv("YATM_TEST_RAW_DIRECTORY")
	if directory == "" {
		t.Skip("set YATM_TEST_RAW_DIRECTORY to a licensed RAW fixture manifest directory")
	}
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct{ Camera, Path, SHA256, License string }
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, fixture := range fixtures {
		t.Run(filepath.Ext(fixture.Path)+"/"+fixture.Camera, func(t *testing.T) {
			// Verify fixture identity before exercising native code on its bytes.
			if fixture.License != "CC0-1.0" {
				t.Fatalf("unapproved fixture license %q", fixture.License)
			}
			file, err := os.Open(fixture.Path)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.New()
			_, err = io.Copy(digest, file)
			_ = file.Close()
			if err != nil {
				t.Fatal(err)
			}
			if hex.EncodeToString(digest.Sum(nil)) != fixture.SHA256 {
				t.Fatal("fixture checksum mismatch")
			}
			seen[filepath.Ext(fixture.Path)[1:]] = true

			// Each actual container must produce a bounded decodable image using the common publication role.
			request := previewprotocol.Request{Kind: "image", SourcePath: fixture.Path, OutputDir: t.TempDir(), Format: "png", Quality: 80, MaxWidth: 512, MaxHeight: 512, MaxInputPixels: 64_000_000}
			var phases []string
			started := time.Now()
			assets, err := generate(context.Background(), request, func(phase string, _, _ int) error { phases = append(phases, phase); return nil })
			if err != nil {
				t.Fatal(err)
			}
			if len(assets) != 1 || assets[0].Role != "thumbnail" || assets[0].Width == 0 || assets[0].Height == 0 || assets[0].Width > 512 || assets[0].Height > 512 {
				t.Fatalf("invalid RAW assets: %+v", assets)
			}
			decoded, err := openMedia(context.Background(), filepath.Join(request.OutputDir, assets[0].Name), false, 64_000_000)
			if err != nil {
				t.Fatal(err)
			}
			defer decoded.close()
			if _, err := decoded.sample(context.Background(), 0, false); err != nil {
				t.Fatal(err)
			}
			pixels, err := decoded.render(128, 128, false)
			if err != nil {
				t.Fatal(err)
			}
			visible := false
			for y := 0; y < pixels.Bounds().Dy(); y++ {
				for x := 0; x < pixels.Bounds().Dx(); x++ {
					c := pixels.NRGBAAt(x, y)
					visible = visible || c.R > 0 || c.G > 0 || c.B > 0
				}
			}
			if !visible {
				t.Fatal("non-black camera fixture rendered black")
			}
			t.Logf("camera=%s elapsed=%s phases=%v", fixture.Camera, time.Since(started), phases)
		})
	}
	for _, extension := range rawExtensions {
		if !seen[extension] {
			t.Errorf("missing representative %s fixture", extension)
		}
	}
}
