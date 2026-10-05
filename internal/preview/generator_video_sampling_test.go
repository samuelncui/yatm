package preview

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVideoTimelineUsesActualSparseKeyframeTimes(t *testing.T) {
	// Encode two six-second GOPs so intermediate requested samples resolve to repeated keyframes.
	helper := testPreviewHelper(t)
	ffmpeg := testFFmpeg(t)
	root := t.TempDir()
	source := filepath.Join(root, "sparse.mp4")
	output, err := exec.Command(
		ffmpeg, "-y", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=128x96:rate=10",
		"-t", "12", "-c:v", "libx264", "-g", "60", "-keyint_min", "60", "-sc_threshold", "0", "-bf", "2", source,
	).CombinedOutput()
	require.NoError(t, err, string(output))
	generator, err := newVideoGenerator(map[string]any{
		"command": helper, "format": "png", "timeline_width": 45, "timeline_height": 81,
		"timeline_interval_seconds": 1, "timeline_max_frames": 12,
	})
	require.NoError(t, err)
	destination := filepath.Join(root, "output")
	require.NoError(t, os.Mkdir(destination, 0o700))

	// Deduplicate repeated keyframes and expose their actual PTS boundaries in the playback map.
	assets, err := generator.Generate(context.Background(), source, destination)
	require.NoError(t, err)
	require.Len(t, assets, 3)
	require.EqualValues(t, 90, assets[1].Width)
	require.EqualValues(t, 81, assets[1].Height)
	mapping, err := os.ReadFile(filepath.Join(destination, "timeline.vtt"))
	require.NoError(t, err)
	contents := string(mapping)
	require.Equal(t, 2, strings.Count(contents, "NOTE sampled at"))
	require.Contains(t, contents, "NOTE sampled at 00:00:00.000")
	require.Contains(t, contents, "NOTE sampled at 00:00:06.000")
	require.Contains(t, contents, "00:00:00.000 --> 00:00:06.000")
	require.Contains(t, contents, "00:00:06.000 --> 00:00:12.000")
}
