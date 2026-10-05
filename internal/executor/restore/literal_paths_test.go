package restore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestRestorePreservesLiteralUTF8(t *testing.T) {
	// Read actual Volume files with distinct literal names through the normal Restore transfer path.
	names := []string{"normal", `back\slash`, `literal\n`, "literal\n", " leading", "trailing ", " \t\n", `quotes'"`, "100%?#", "照片 😀"}
	files := make(map[string][]byte, len(names))
	for index, name := range names {
		files[" folder\\ /"+name] = []byte(fmt.Sprintf("saved content %d", index))
	}
	runner, volume, candidates := setupVolumeRestore(t, "literal", files)
	ctx := context.Background()
	_, err := (&service{exe: runner.exe}).RestoreMedia(ctx, &entity.RestoreMediaRequest{
		Id: runner.job.ID, Target: volume.Pack(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !runner.exe.IsRunning(runner.job.ID) }, 10*time.Second, 10*time.Millisecond)
	job, err := runner.exe.GetJob(ctx, runner.job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, job.Status, job.Error)

	// Output bytes, manifest paths and published originals retain exactly the frozen spelling.
	for _, candidate := range candidates {
		want, exists := files[candidate.MediaPath]
		require.True(t, exists, candidate.MediaPath)
		require.Equal(t, "Unforged/literal/"+candidate.MediaPath, candidate.TargetPath)
		content, err := os.ReadFile(runner.restoreTarget(candidate.TargetPath))
		require.NoError(t, err)
		require.Equal(t, want, content)
		var output File
		require.NoError(t, runner.db.First(&output, candidate.ItemID).Error)
		require.Equal(t, candidate.TargetPath, output.Path)
		original, err := runner.exe.Lib().GetFileLocation(ctx, output.FileID)
		require.NoError(t, err)
		require.NotNil(t, original)
		require.Equal(t, candidate.TargetPath, original.Path)
	}
}
