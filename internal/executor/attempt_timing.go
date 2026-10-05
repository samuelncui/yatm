package executor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

func (e *Executor) startAttemptTiming(ctx context.Context, jobID int64) error {
	// Read the complete Job record created with the bundle.
	db, closeDB, err := e.openStateDB(jobID)
	if err != nil {
		return err
	}
	defer closeDB()
	// Timing starts only for newly created preparation or an admitted READY Media operation.
	var record JobRecord
	if err := db.WithContext(ctx).First(&record, singletonJobID).Error; err != nil {
		return fmt.Errorf("record Job attempt start failed, id=%d, %w", jobID, err)
	}
	if record.Status != entity.JobStatus_JOB_STATUS_PREPARING && record.Status != entity.JobStatus_JOB_STATUS_READY {
		return fmt.Errorf("Job cannot start from status %s, id=%d", record.Status, jobID)
	}

	// A new Media operation replaces the previous operation's timing and error without changing state.
	startedAt := time.Now()
	result := db.WithContext(ctx).Model(&JobRecord{}).Where("id = ?", singletonJobID).Updates(map[string]any{
		"latest_attempt_started_at_ns":  startedAt.UnixNano(),
		"latest_attempt_finished_at_ns": nil,
		"error":                         "",
	})
	if result.Error != nil {
		return fmt.Errorf("record Job attempt start failed, id=%d, %w", jobID, result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("record Job attempt start failed, id=%d, common record missing", jobID)
	}

	// Keep the monotonic clock component for live and final elapsed calculations.
	e.attemptsLock.Lock()
	e.attempts[jobID].startedAt = startedAt
	e.attemptsLock.Unlock()
	return nil
}

func (e *Executor) finishAttemptTiming(jobID int64, runner Runner) {
	// Convert monotonic elapsed time into the durable end boundary despite wall-clock changes.
	e.attemptsLock.Lock()
	startedAt := e.attempts[jobID].startedAt
	e.attemptsLock.Unlock()
	finishedAt := startedAt.Add(time.Since(startedAt)).UnixNano()

	// Runner cancellation must not prevent recording the completed attempt.
	db, closeDB, err := e.openStateDB(jobID)
	if err != nil {
		runner.Logger().WithError(err).Errorf("finish Job attempt timing failed, id=%d", jobID)
		return
	}
	defer closeDB()
	result := db.Model(&JobRecord{}).Where("id = ?", singletonJobID).
		Update("latest_attempt_finished_at_ns", finishedAt)
	if result.Error != nil {
		runner.Logger().WithError(result.Error).Errorf("finish Job attempt timing failed, id=%d", jobID)
		return
	}
	if result.RowsAffected != 1 {
		runner.Logger().Errorf("finish Job attempt timing failed, id=%d, common record missing", jobID)
	}
}

// ApplyAttemptProgress adds elapsed time for the latest attempt without counting time waiting for work.
// Callers hold the Job read lock so a new attempt cannot replace the observed timing.
func (e *Executor) ApplyAttemptProgress(ctx context.Context, jobID int64, progress *entity.Progress) error {
	// Live timing comes from the server's monotonic clock, independent of polling or catalog updates.
	progress.ElapsedMs = nil
	e.attemptsLock.Lock()
	current := e.attempts[jobID]
	if current != nil && !current.startedAt.IsZero() {
		progress.ElapsedMs = proto.Int64(time.Since(current.startedAt).Milliseconds())
		e.attemptsLock.Unlock()
		return nil
	}
	e.attemptsLock.Unlock()

	// An abruptly interrupted attempt has no known final duration.
	db, closeDB, err := e.openStateDB(jobID)
	if err != nil {
		return err
	}
	defer closeDB()
	var record JobRecord
	if err := db.WithContext(ctx).First(&record, singletonJobID).Error; err != nil {
		return fmt.Errorf("read Job attempt timing failed, id=%d, %w", jobID, err)
	}
	// Cached runner phases describe retained work; only an active attempt exposes a live phase.
	if current == nil {
		if progress.Stage != nil {
			progress.Stage = &entity.StageProgress{Phase: entity.JobPhase_JOB_PHASE_UNSPECIFIED,
				Unit: progress.Stage.Unit, Completed: progress.Stage.Completed,
				EstimateState: entity.EstimateState_ESTIMATE_STATE_NOT_APPLICABLE}
		}
		if record.Kind == entity.JobKind_JOB_KIND_ARCHIVE || record.Kind == entity.JobKind_JOB_KIND_RESTORE {
			checkpoint := manifestPosition(record.Checkpoint)
			progress.TotalKnown = checkpoint == entity.JobStatus_JOB_STATUS_READY || checkpoint == entity.JobStatus_JOB_STATUS_COMPLETED
		}
	}
	if record.LatestAttemptStartedAtNS != nil && record.LatestAttemptFinishedAtNS != nil {
		progress.ElapsedMs = proto.Int64(max(0, (*record.LatestAttemptFinishedAtNS-*record.LatestAttemptStartedAtNS)/int64(time.Millisecond)))
	}
	return nil
}

// recordAttemptResult publishes the ended operation and its reason together. Preparation and Scan
// failures are terminal; Archive and Restore Media failures return to their pre-operation state.
func (e *Executor) recordAttemptResult(
	ctx context.Context, jobID int64, runner Runner, failure error, failureState entity.JobStatus,
) {
	// Store the result in the existing Job record even after cancellation.
	db, closeDB, err := e.openStateDB(jobID)
	if err != nil {
		runner.Logger().WithError(err).Error("record Job result failed")
		return
	}
	defer closeDB()
	var record JobRecord
	if err := db.First(&record, singletonJobID).Error; err != nil {
		runner.Logger().WithError(err).Error("record Job result failed")
		return
	}
	// Successful publications survive a later Media cleanup error; otherwise use the operation's
	// failure state. No failed Job is promoted back to its retained manifest checkpoint.
	state, message := record.Status, ""
	if failure != nil {
		state, message = failureState, attemptFailureReason(ctx, failure)
		if failureState == entity.JobStatus_JOB_STATUS_READY && record.Status == entity.JobStatus_JOB_STATUS_COMPLETED {
			state = record.Status
		}
	}
	updated := db.Model(&JobRecord{}).Where("id = ?", singletonJobID).
		Updates(map[string]any{"status": state, "error": message})
	if updated.Error != nil {
		runner.Logger().WithError(updated.Error).Error("record Job result failed")
		return
	}
	// Publish the changed state to Job list consumers.
	e.TouchJob(jobID)
}

// attemptFailureReason is the reason a settled attempt records. The runner reports the error the
// interrupted work produced, which for a cancellation is the context error and names no actor; the
// cause on the attempt context names one, so the operator's own action records as
// attemptCancelledByOperator instead of the internal error.
func attemptFailureReason(ctx context.Context, failure error) string {
	if errors.Is(failure, context.Canceled) && errors.Is(context.Cause(ctx), errCancelledByOperator) {
		return attemptCancelledByOperator
	}
	return failure.Error()
}
