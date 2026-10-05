package restore

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

// TestRestoreCopyEstimateComesFromACPWorkEvents pins the sampling contract of the copy stage: the
// ACP progress events alone produce the estimate, and a progress request only reads the window they
// recorded, so a card that never polls cannot freeze it and a polling cadence cannot change it.
func TestRestoreCopyEstimateComesFromACPWorkEvents(t *testing.T) {
	exe, lib := setupTestExecutor(t)
	media := newTapeMedia("STAGE")
	fileIDs := make([]int64, 0, 4)
	for index := 0; index < 4; index++ {
		_, file := createMediaFile(t, lib, media, "", fmt.Sprintf("file-%d", index), []byte("0123456789"), nil)
		fileIDs = append(fileIDs, file.ID)
	}
	job := createRestoreJob(t, exe, fileIDs...)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(context.Background(), job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	// Reach the copying phase through the same transitions a Media attempt makes.
	require.NoError(t, runner.transition(restoreStatePreparingMedia))
	require.NoError(t, runner.transition(restoreStateCopying))

	// A fixed clock makes every sample an exact five-second productive window. The attempt stays
	// incomplete on purpose, so a remaining time exists to be estimated.
	now := time.Unix(100, 0)
	progress := runner.getProgress()
	progress.SetClock(func() time.Time { return now })
	handler := runner.restoreEventHandler(context.Background())
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
