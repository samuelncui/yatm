package demo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"image"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/samuelncui/yatm/internal/library"
	previewpkg "github.com/samuelncui/yatm/internal/preview"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func requireBundledVideoPreviews(t *testing.T, previews *previewpkg.Manager, signature []byte) {
	// Read the carried native output through the normal manifest and bundle asset readers.
	t.Helper()
	manifest, err := previews.Manifest(signature)
	require.NoError(t, err)
	require.Equal(t, signature, manifest.FileSignature)
	require.Len(t, manifest.Assets, 3)
	for index, want := range []struct {
		role          string
		width, height uint32
		data          []byte
	}{
		{"poster", 512, 288, bundledVideoPoster},
		{"timeline", 3200, 540, bundledVideoTimeline},
		{"timeline-map", 0, 0, bundledVideoTimelineMap},
	} {
		asset := manifest.Assets[index]
		require.Equal(t, want.role, asset.Role)
		require.Equal(t, want.width, asset.WidthPx)
		require.Equal(t, want.height, asset.HeightPx)
		reader, err := previews.Open(signature, want.role)
		require.NoError(t, err)
		data, err := io.ReadAll(reader)
		require.NoError(t, reader.Close())
		require.NoError(t, err)
		require.Equal(t, want.data, data)
	}
}

func TestBundledVideoTimelineUsesDistinctNativeFrames(t *testing.T) {
	// Decode real derivative images and keep the complete embedded asset set small.
	poster, err := png.Decode(bytes.NewReader(bundledVideoPoster))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 512, 288), poster.Bounds())
	timeline, err := png.Decode(bytes.NewReader(bundledVideoTimeline))
	require.NoError(t, err)
	require.Equal(t, image.Rect(0, 0, 3200, 540), timeline.Bounds())
	require.LessOrEqual(t,
		len(bundledVideo)+len(bundledVideoPoster)+len(bundledVideoTimeline)+len(bundledVideoTimelineMap),
		20<<20,
	)

	// Every VTT cue covers the next ten seconds of the five-minute excerpt across three sprite rows.
	require.True(t, bytes.HasPrefix(bundledVideoTimelineMap, []byte("WEBVTT\n\n")))
	cues := regexp.MustCompile(
		`(?m)^00:(\d{2}):(\d{2})\.000 --> 00:(\d{2}):(\d{2})\.000\n`+
			`timeline\.png#xywh=(\d+),(\d+),(\d+),(\d+)$`,
	).FindAllSubmatch(bundledVideoTimelineMap, -1)
	require.Len(t, cues, 30)
	frames := make(map[[32]byte]bool, len(cues))
	for index, cue := range cues {
		values := make([]int, 0, 8)
		for _, field := range cue[1:] {
			value, err := strconv.Atoi(string(field))
			require.NoError(t, err)
			values = append(values, value)
		}
		require.Equal(t, index*10, values[0]*60+values[1])
		require.Equal(t, (index+1)*10, values[2]*60+values[3])
		require.Equal(t, []int{index % 10 * 320, index / 10 * 180, 320, 180}, values[4:])
		rect := image.Rect(values[4], values[5], values[4]+values[6], values[5]+values[7])
		require.True(t, rect.In(timeline.Bounds()))
		tile := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
		draw.Draw(tile, tile.Bounds(), timeline, rect.Min, draw.Src)
		hash := sha256.Sum256(tile.Pix)
		require.False(t, frames[hash], "each sampled frame must show distinct source content")
		frames[hash] = true
	}
}

func TestBundledVideoPreviewRejectsDifferentSource(t *testing.T) {
	// A changed file must never receive unrelated carried derivatives.
	root := t.TempDir()
	source := filepath.Join(root, demoVideoName)
	require.NoError(t, os.WriteFile(source, []byte("different video content"), 0o644))
	assets, err := (demoVideoGenerator{}).Generate(context.Background(), source, root)
	require.ErrorContains(t, err, "requires the bundled excerpt")
	require.Empty(t, assets)
	require.NoFileExists(t, filepath.Join(root, "poster.png"))
}

func TestBundledVideoAssetsReproduceWithNativeWorker(t *testing.T) {
	// Native reproduction is explicit; ordinary Demo tests need no installed helper.
	helper := os.Getenv("YATM_TEST_PREVIEW_HELPER")
	if helper == "" {
		t.Skip("set YATM_TEST_PREVIEW_HELPER to reproduce the bundled native assets")
	}
	root := t.TempDir()
	source := filepath.Join(root, demoVideoName)
	require.NoError(t, os.WriteFile(source, bundledVideo, 0o644))
	previews, err := previewpkg.New(previewpkg.Config{
		Root: filepath.Join(root, "previews"),
		Generators: []previewpkg.GeneratorConfig{{Kind: "video", Extensions: []string{"mp4"}, Options: map[string]any{
			"command": helper, "format": "png",
		}}},
	}, root)
	require.NoError(t, err)

	// Production generation validates and publishes the actual worker result in the ordinary bundle layout.
	hash := sha256.Sum256(bundledVideo)
	signature, err := previews.Generate(context.Background(), source, hash[:], int64(len(bundledVideo)), 0, true, nil)
	require.NoError(t, err)
	requireBundledVideoPreviews(t, previews, signature)
}

func TestPrepareVideoOverrideUsesNativeWorker(t *testing.T) {
	// Keep the supported override on the real worker path rather than carrying the bundled sprite.
	helper := os.Getenv("YATM_TEST_PREVIEW_HELPER")
	if helper == "" {
		t.Skip("set YATM_TEST_PREVIEW_HELPER to verify the video override")
	}
	helper, err := filepath.Abs(helper)
	require.NoError(t, err)
	require.Equal(t, "yatm-preview", filepath.Base(helper))
	t.Setenv("PATH", filepath.Dir(helper)+string(os.PathListSeparator)+os.Getenv("PATH"))
	parent := t.TempDir()
	source := filepath.Join(parent, "override.mp4")
	require.NoError(t, os.WriteFile(source, bundledVideo, 0o644))
	root := filepath.Join(parent, "yatm-demo-override")
	require.NoError(t, Prepare(context.Background(), Options{Root: root, Listen: "127.0.0.1:18080", Reset: true, VideoPath: source}))

	// The production defaults produce the same thirty-frame native bundle for an explicit override.
	db, err := resource.OpenSQLite(filepath.Join(root, databaseName))
	require.NoError(t, err)
	video, err := library.New(db).GetByPath(context.Background(), library.Root.ID, "Photos/"+demoVideoName)
	require.NoError(t, err)
	previews, err := previewpkg.New(previewpkg.Config{Root: filepath.Join(root, "previews")}, filepath.Join(root, "work"))
	require.NoError(t, err)
	manifest, err := previews.Manifest(video.Signature)
	require.NoError(t, err)
	require.Equal(t, "video", manifest.Generator)
	requireBundledVideoPreviews(t, previews, video.Signature)

	// Reuse retains the prepared override and still requires an explicit reset to replace its source.
	require.NoError(t, Prepare(context.Background(), Options{Root: root, Listen: "127.0.0.1:18080"}))
	require.ErrorContains(t, Prepare(context.Background(), Options{Root: root, Listen: "127.0.0.1:18080", VideoPath: source}), "override requires reset")
}
