package archive

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/stretchr/testify/require"
)

// TestArchiveCopyEstimateComesFromACPWorkEvents pins the sampling contract of the copy stage: the
// ACP progress events alone produce the estimate, and a progress request only reads the window they
// recorded, so a card that never polls cannot freeze it and a polling cadence cannot change it.
func TestArchiveCopyEstimateComesFromACPWorkEvents(t *testing.T) {
	exe := setupTestExecutor(t, executor.Scripts{})
	root := filepath.Join(exe.Paths().Source, "stage")
	require.NoError(t, os.Mkdir(root, 0o755))
	for index := 0; index < 4; index++ {
		require.NoError(t, os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%d", index)), []byte("0123456789"), 0o644))
	}
	job := createArchiveJob(t, exe, &archiveTestSource{Base: exe.Paths().Source, Path: []string{"stage"}})
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(context.Background(), job.ID)
	require.NoError(t, err)
	runner := value.(*jobArchiveRunner)
	// Reach the copying phase through the same transitions a Media attempt makes.
	require.NoError(t, runner.transition(archiveStatePreparingMedia))
	require.NoError(t, runner.transition(archiveStateCopying))

	// A fixed clock makes every sample an exact five-second productive window. The attempt stays
	// incomplete on purpose, so a remaining time exists to be estimated.
	// Initialize the durable base the way a request or a new attempt does, then fix the clock.
	now := time.Unix(100, 0)
	progress := runner.getProgress()
	progress.SetClock(func() time.Time { return now })
	handler := runner.archiveEventHandler(context.Background())
	handler(&acp.EventUpdateProgress{Bytes: 0, Files: 0})
	require.Nil(t, runner.progressSnapshot().Stage.RemainingSeconds, "no estimate before any measured work")

	// Three productive windows of one settled item each: no progress request is involved.
	for step := int64(1); step <= 3; step++ {
		now = now.Add(5 * time.Second)
		handler(&acp.EventUpdateProgress{Bytes: step * 10, Files: step})
	}
	stage := runner.progressSnapshot().Stage
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATED, stage.EstimateState, "work events produced the estimate")
	require.Equal(t, entity.ProgressUnit_PROGRESS_UNIT_BYTES, stage.Unit)
	require.EqualValues(t, 40, *stage.Total)
	require.EqualValues(t, 30, stage.Completed)
	require.Equal(t, 2.0, stage.GetRatePerSecond())
	require.EqualValues(t, 5, stage.GetRemainingSeconds())

	// Requests read the recorded window without refreshing it: a long gap the work path has not
	// sampled yet changes nothing, and the frozen estimate stays readable.
	now = now.Add(10 * time.Minute)
	for index := 0; index < 3; index++ {
		repeated := runner.progressSnapshot().Stage
		require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATED, repeated.EstimateState)
		require.Equal(t, stage.GetRemainingSeconds(), repeated.GetRemainingSeconds())
	}

	// A real stall is detected by the next work event, not by a visible card.
	handler(&acp.EventUpdateProgress{Bytes: 40, Files: 4})
	require.Equal(t, entity.EstimateState_ESTIMATE_STATE_ESTIMATING, runner.progressSnapshot().Stage.EstimateState)
	require.EqualValues(t, 40, runner.progressSnapshot().Stage.Completed)
}
