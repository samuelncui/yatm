package preview

import (
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestBuiltInGenerators(t *testing.T) {
	// Require the real external tools before building media fixtures.
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe is not installed")
	}

	// Generate one deterministic image and video input with ffmpeg.
	root := t.TempDir()
	imagePath := filepath.Join(root, "image.png")
	command := exec.Command(
		ffmpeg, "-y", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=red:s=320x180", "-frames:v", "1", imagePath,
	)
	require.NoError(t, command.Run())
	videoPath := filepath.Join(root, "video.mp4")
	command = exec.Command(
		ffmpeg, "-y", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=10",
		"-t", "2", "-c:v", "mpeg4", "-q:v", "5", videoPath,
	)
	require.NoError(t, command.Run())

	// Configure both built-in generators against the isolated Preview store.
	previews, err := New(Config{Root: filepath.Join(root, "previews"), Generators: []GeneratorConfig{
		{Kind: "image", Extensions: []string{"png"}, Options: map[string]any{"command": ffmpeg, "format": "png"}},
		{Kind: "video", Extensions: []string{"mp4"}, Options: map[string]any{"ffmpeg": ffmpeg, "ffprobe": ffprobe, "format": "png"}},
	}}, "")
	require.NoError(t, err)

	// Generate the image Preview from caller-provided content facts.
	imageData, err := os.ReadFile(imagePath)
	require.NoError(t, err)
	imageInfo, err := os.Stat(imagePath)
	require.NoError(t, err)
	imageHash := sha256.Sum256(imageData)
	imageSignature, err := previews.Generate(
		context.Background(), imagePath, imageHash[:], imageInfo.Size(), imageInfo.ModTime().UnixNano(), false,
	)
	require.NoError(t, err)
	imageManifest, err := previews.Manifest(imageSignature)
	require.NoError(t, err)
	require.Equal(t, []string{"thumbnail"}, assetRoles(imageManifest.Assets))

	// Generate the video Preview and validate its complete role set.
	videoData, err := os.ReadFile(videoPath)
	require.NoError(t, err)
	videoInfo, err := os.Stat(videoPath)
	require.NoError(t, err)
	videoHash := sha256.Sum256(videoData)
	videoSignature, err := previews.Generate(
		context.Background(), videoPath, videoHash[:], videoInfo.Size(), videoInfo.ModTime().UnixNano(), false,
	)
	require.NoError(t, err)
	videoManifest, err := previews.Manifest(videoSignature)
	require.NoError(t, err)
	require.Equal(t, []string{"poster", "timeline", "timeline-map"}, assetRoles(videoManifest.Assets))
}

func assetRoles(assets []*entity.PreviewAsset) []string {
	roles := make([]string, 0, len(assets))
	for _, asset := range assets {
		roles = append(roles, asset.Role)
	}
	return roles
}
