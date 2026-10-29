package executor

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm/logger"
)

type snapshotRunner struct {
	*testRunner
	phase atomic.Int32
}

func (r *snapshotRunner) Phase() entity.JobPhase { return entity.JobPhase(r.phase.Load()) }

func (r *snapshotRunner) transition(phase entity.JobPhase) {
	r.phase.Store(int32(phase))
	r.exe.TouchJob(r.job.ID)
}

func TestAttemptSnapshotRetainsLivePhaseUntilSettlement(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "waiting"
		if completed {
			name = "completed"
		}
		t.Run(name, func(t *testing.T) {
			// Replace only the runner's phases; admission, records and publication stay real.
			ctx := context.Background()
			exe := setupTestExecutor(t)
			job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID,
				entity.JobStatus_JOB_STATUS_READY)
			runner := &snapshotRunner{testRunner: exe.runners[job.ID].(*testRunner)}
			exe.runners[job.ID] = runner
			returned, release := make(chan struct{}), make(chan struct{})
			var released sync.Once
			t.Cleanup(func() { released.Do(func() { close(release) }); waitJobReleased(t, exe, job.ID) })
			require.NoError(t, exe.StartJob(ctx, job.ID, job.Kind, func(ctx context.Context, _ Runner) error {
				runner.transition(entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA)
				phase := entity.JobPhase_JOB_PHASE_UNSPECIFIED
				if completed {
					if err := exe.UpdateJobStatus(ctx, job.ID, job.Status, entity.JobStatus_JOB_STATUS_COMPLETED); err != nil {
						return err
					}
					phase = entity.JobPhase_JOB_PHASE_COMPLETED
				}
				runner.transition(phase)
				close(returned)
				<-release
				return errors.New("Media cleanup failed")
			}))
			select {
			case <-returned:
			case <-time.After(5 * time.Second):
				t.Fatal("runner did not reach its return boundary")
			}

			// Get, incremental List and typed progress must keep consumers waiting for the result.
			stored, err := exe.GetJob(ctx, job.ID)
			require.NoError(t, err)
			require.Equal(t, entity.JobPhase_JOB_PHASE_FINALIZING_MEDIA, stored.Phase)
			require.Empty(t, stored.Error)
			page, err := exe.ListJob(ctx, &entity.JobFilter{ChangedAfterRevision: &job.Revision})
			require.NoError(t, err)
			require.Len(t, page.Jobs, 1)
			require.Equal(t, stored.Phase, page.Jobs[0].Phase)
			progress := &entity.Progress{Stage: &entity.StageProgress{Phase: runner.Phase()}}
			require.NoError(t, exe.ApplyAttemptProgress(ctx, job.ID, progress))
			require.Equal(t, stored.Phase, progress.Stage.Phase)
			require.NotNil(t, progress.ElapsedMs)

			// The next idle observation includes the settled error and final timing.
			released.Do(func() { close(release) })
			waitJobReleased(t, exe, job.ID)
			settled, err := exe.GetJob(ctx, job.ID)
			require.NoError(t, err)
			require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, settled.Phase)
			require.Equal(t, "Media cleanup failed", settled.Error)
			require.NotNil(t, attemptProgress(t, exe, job.ID).ElapsedMs)
		})
	}
}

func TestAttemptSnapshotAdmissionHasLivePhaseBeforeTiming(t *testing.T) {
	// Admission owns execution before the durable clock or the runner's first transition.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID,
		entity.JobStatus_JOB_STATUS_READY)
	exe.runners[job.ID] = &snapshotRunner{testRunner: exe.runners[job.ID].(*testRunner)}
	require.NoError(t, exe.beginAttempt(job.ID, func(error) {}))
	t.Cleanup(func() { exe.endAttempt(job.ID) })

	// Neither a retained idle runner nor an unset start time can expose an idle attempt.
	stored, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobPhase_JOB_PHASE_QUEUED, stored.Phase)
	progress := &entity.Progress{Stage: &entity.StageProgress{}}
	require.NoError(t, exe.ApplyAttemptProgress(ctx, job.ID, progress))
	require.Equal(t, stored.Phase, progress.Stage.Phase)
}

type snapshotReadKey struct{}

type snapshotReadLogger struct {
	logger.Interface
	read func()
}

func (l snapshotReadLogger) Trace(ctx context.Context, begin time.Time, query func() (string, int64), err error) {
	// Intercept only the marked hydration query after its old JobRecord has been read.
	sql, rows := query()
	l.Interface.Trace(ctx, begin, func() (string, int64) { return sql, rows }, err)
	if ctx.Value(snapshotReadKey{}) != nil && strings.Contains(sql, "FROM `job`") {
		l.read()
	}
}

func TestAttemptSnapshotCannotPairOldRecordWithNewIdlePhase(t *testing.T) {
	// Freeze one old-record read while its active attempt is allowed to settle completely.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID,
		entity.JobStatus_JOB_STATUS_READY)
	finish, read, resume := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var finished, resumed sync.Once
	t.Cleanup(func() {
		finished.Do(func() { close(finish) })
		resumed.Do(func() { close(resume) })
		waitJobReleased(t, exe, job.ID)
	})
	require.NoError(t, exe.StartJob(ctx, job.ID, job.Kind, func(context.Context, Runner) error {
		<-finish
		return errors.New("Media is full")
	}))
	previous := logger.Default
	logger.Default = snapshotReadLogger{Interface: previous, read: func() { close(read); <-resume }}
	t.Cleanup(func() { logger.Default = previous })
	observed := make(chan *Job, 1)
	readError := make(chan error, 1)
	go func() {
		value, err := exe.GetJob(context.WithValue(ctx, snapshotReadKey{}, true), job.ID)
		observed <- value
		readError <- err
	}()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("hydration did not read the old record")
	}

	// End the attempt after the old record was read, before hydration returns to its caller.
	finished.Do(func() { close(finish) })
	waitJobReleased(t, exe, job.ID)
	resumed.Do(func() { close(resume) })
	stored := <-observed
	require.NoError(t, <-readError)
	require.Empty(t, stored.Error)
	require.Equal(t, entity.JobPhase_JOB_PHASE_QUEUED, stored.Phase,
		"the old record must retain its active observation rather than become actionable")

	// A later idle snapshot reads the final result without holding a global lock across its query.
	settled, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, settled.Phase)
	require.Equal(t, "Media is full", settled.Error)
}
