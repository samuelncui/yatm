package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

type testRunner struct {
	exe      *Executor
	job      *Job
	indexErr error
	logger   *logrus.Logger
}

type unavailableRunner struct {
	testRunner
	err error
}

func (r *unavailableRunner) SetActive(bool) error { return r.err }

func TestAttemptResourceFailureIsNotReportedAsBusy(t *testing.T) {
	// Admission must retain a resource error without registering an attempt or masking it as contention.
	exe := setupTestExecutor(t)
	failure := errors.New("runner files unavailable")
	exe.runners[7] = &unavailableRunner{err: failure}
	err := exe.beginAttempt(7, func(error) {})
	require.ErrorIs(t, err, failure)
	require.NotErrorIs(t, err, ErrJobBusy)
	require.False(t, exe.IsRunning(7))
}

func (r *testRunner) Index(ctx context.Context) error {
	if r.indexErr != nil {
		return r.indexErr
	}
	return r.exe.UpdateJobStatus(ctx, r.job.ID, entity.JobStatus_JOB_STATUS_PREPARING, entity.JobStatus_JOB_STATUS_READY)
}

func (r *testRunner) Phase() entity.JobPhase {
	if r.indexErr != nil {
		return entity.JobPhase_JOB_PHASE_UNSPECIFIED
	}
	return entity.JobPhase_JOB_PHASE_QUEUED
}

func (*testRunner) Close() error { return nil }

func (r *testRunner) Logger() *logrus.Logger {
	if r.logger == nil {
		r.logger = logrus.New()
	}
	return r.logger
}

func init() {
	register := func(grpc.ServiceRegistrar, *Executor) {}
	RegisterJobType(entity.JobKind_JOB_KIND_ARCHIVE, func(_ context.Context, exe *Executor, job *Job) (Runner, error) {
		return &testRunner{exe: exe, job: job}, nil
	}, register)
	RegisterJobType(entity.JobKind_JOB_KIND_RESTORE, func(_ context.Context, exe *Executor, job *Job) (Runner, error) {
		return &testRunner{exe: exe, job: job, indexErr: errors.New("injected index failure")}, nil
	}, register)
}

func setupTestExecutor(t *testing.T) *Executor {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "executor.db"))
	require.NoError(t, err)
	settings := settingspkg.New(db, settingspkg.PreviewDefinition{})
	lib := library.NewWithSettings(db, settings)
	exe := New(db, lib, []string{"/dev/nst0"}, Paths{Work: t.TempDir()}, Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	require.NoError(t, settings.AutoMigrate())
	return exe
}

func createTestCatalogJob(t *testing.T, exe *Executor, kind entity.JobKind) *Job {
	t.Helper()
	row := &jobCatalogRow{ExecutorID: localExecutorID, CatalogKind: kind}
	require.NoError(t, exe.db.Create(row).Error)
	return row.snapshot()
}

func createTestJob(t *testing.T, exe *Executor, kind entity.JobKind, priority int64) *Job {
	t.Helper()
	job, err := exe.CreateJob(context.Background(), kind, priority, func(db *gorm.DB) error {
		table := "items"
		if kind == entity.JobKind_JOB_KIND_RESTORE {
			table = "copies"
		}
		if err := db.Exec("CREATE TABLE " + table + " (id INTEGER PRIMARY KEY)").Error; err != nil {
			return err
		}
		// A bundle whose manifest is not built yet carries the config the initial preparation builds it from.
		if err := db.Exec("CREATE TABLE config (id INTEGER PRIMARY KEY)").Error; err != nil {
			return err
		}
		return db.Exec("INSERT INTO config (id) VALUES (1)").Error
	})
	require.NoError(t, err)
	return job
}

func waitJobStatus(t *testing.T, exe *Executor, id int64, status entity.JobStatus) *Job {
	t.Helper()
	var job *Job
	require.Eventually(t, func() bool {
		var err error
		job, err = exe.GetJob(context.Background(), id)
		return err == nil && job.Status == status && !exe.IsRunning(id)
	}, time.Second, time.Millisecond)
	return job
}

func waitJobState(t *testing.T, exe *Executor, id int64, state entity.JobStatus) *Job {
	t.Helper()
	var job *Job
	require.Eventually(t, func() bool {
		stored, err := exe.GetJob(context.Background(), id)
		if err != nil || stored.Status != state {
			return false
		}
		job = stored
		return true
	}, 5*time.Second, time.Millisecond)
	// A settled failure is never observable without the reason it recorded: the runner unwinds and
	// the executor persists the state in different goroutines, so this is the ordering under test.
	if state == entity.JobStatus_JOB_STATUS_FAILED {
		require.NotEmpty(t, job.Error, "a settled attempt published FAILED without a reason")
	}
	return job
}

func jobState(t *testing.T, exe *Executor, id int64) entity.JobStatus {
	t.Helper()
	stored, err := exe.GetJob(context.Background(), id)
	require.NoError(t, err)
	return stored.Status
}

func jobAttemptStartedAt(t *testing.T, exe *Executor, id int64) int64 {
	t.Helper()
	db, closeDB, err := exe.openStateDB(id)
	require.NoError(t, err)
	defer closeDB()
	var record JobRecord
	require.NoError(t, db.First(&record, singletonJobID).Error)
	require.NotNil(t, record.LatestAttemptStartedAtNS)
	return *record.LatestAttemptStartedAtNS
}

func waitJobReleased(t *testing.T, exe *Executor, id int64) {
	t.Helper()
	require.Eventually(t, func() bool { return !exe.IsRunning(id) }, 5*time.Second, time.Millisecond)
}

func TestFailedAttemptPublishesItsStateWithItsReason(t *testing.T) {
	// The runner unwinds and the executor records the durable result in different goroutines. A
	// reader keyed on a phase used to observe the actionable state before the error was written;
	// FAILED and its reason must instead become observable in one step.
	exe := setupTestExecutor(t)
	job := createTestJob(t, exe, entity.JobKind_JOB_KIND_RESTORE, 0)

	failed := waitJobState(t, exe, job.ID, entity.JobStatus_JOB_STATUS_FAILED)
	require.Contains(t, failed.Error, "injected index failure")
	require.Equal(t, entity.JobStatus_JOB_STATUS_PREPARING, failed.Checkpoint, "a failure keeps the manifest checkpoint")
	waitJobReleased(t, exe, job.ID)
}

func TestCancelledArchiveMediaRetainsReasonAndAllowsAnotherMediaOperation(t *testing.T) {
	// A cancelled Media operation returns to selection and retains its reason until the next operation.
	exe := setupTestExecutor(t)
	job := createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0)
	waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)

	startAttempt := func() {
		t.Helper()
		started := make(chan struct{})
		require.NoError(t, exe.StartJob(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE,
			func(ctx context.Context, _ Runner) error {
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}))
		<-started
		// Execution is a live observation: the durable state stays at the manifest checkpoint.
		require.Equal(t, entity.JobStatus_JOB_STATUS_READY, jobState(t, exe, job.ID))
	}

	startAttempt()
	require.NoError(t, exe.Cancel(job.ID))
	stopped := waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
	require.Contains(t, stopped.Error, attemptCancelledByOperator)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, stopped.Checkpoint)
	waitJobReleased(t, exe, job.ID)

	// Another explicit Media operation uses the prepared manifest without restarting the Job.
	startAttempt()
	stored, err := exe.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Empty(t, stored.Error)
	require.NoError(t, exe.Cancel(job.ID))
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY).Status)
	waitJobReleased(t, exe, job.ID)
}

func TestInterruptedAttemptKeepsNoDurableRunningTrace(t *testing.T) {
	// Execution is a live observation, so a Job whose process disappeared has no durable trace of
	// running: it keeps the state its attempt settled, and the missing finish boundary is read as an
	// unknown duration rather than as downtime.
	exe := setupTestExecutor(t)
	job := createTestJob(t, exe, entity.JobKind_JOB_KIND_RESTORE, 0)
	waitJobState(t, exe, job.ID, entity.JobStatus_JOB_STATUS_FAILED)
	waitJobReleased(t, exe, job.ID)

	// Simulate the process disappearing mid-attempt: no finish boundary was ever written.
	db, closeDB, err := exe.openStateDB(job.ID)
	require.NoError(t, err)
	require.NoError(t, db.Model(&JobRecord{}).Where("id = ?", singletonJobID).Update("latest_attempt_finished_at_ns", nil).Error)
	closeDB()

	require.NoError(t, exe.ReconcileStorage(context.Background()))
	stored, err := exe.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, stored.Status)
	require.Equal(t, entity.JobStatus_JOB_STATUS_PREPARING, stored.Checkpoint)
	require.Nil(t, attemptProgress(t, exe, job.ID).ElapsedMs, "an interrupted attempt has no known duration")
}

func TestExecutorJobCatalogAndBundle(t *testing.T) {
	exe := setupTestExecutor(t)
	created := createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 7)
	created = waitJobStatus(t, exe, created.ID, entity.JobStatus_JOB_STATUS_READY)
	require.Equal(t, entity.JobKind_JOB_KIND_ARCHIVE, created.Kind)
	require.Equal(t, int64(7), created.Priority)
	require.Positive(t, created.CreatedAtNS)
	require.Positive(t, created.UpdatedAtNS)

	var catalog jobCatalogRow
	require.NoError(t, exe.db.First(&catalog, created.ID).Error)
	require.Equal(t, localExecutorID, catalog.ExecutorID)
	require.FileExists(t, exe.stateDBPath(created.ID))
	require.DirExists(t, filepath.Join(exe.jobWorkPath(created.ID), "tapes"))
	require.NoDirExists(t, filepath.Join(exe.jobWorkPath(created.ID), "logs"))

	listed, err := exe.ListJob(context.Background(), &entity.JobFilter{})
	require.NoError(t, err)
	require.Len(t, listed.Jobs, 1)
	require.Equal(t, created.ID, listed.Jobs[0].ID)

	deleted, deleteErr := exe.DeleteJobs(context.Background(), false, created.ID)
	require.NoError(t, deleteErr)
	require.Equal(t, int64(1), deleted)
	_, err = exe.GetJob(context.Background(), created.ID)
	require.ErrorIs(t, err, ErrJobNotFound)
	require.NoFileExists(t, exe.stateDBPath(created.ID))
}

func TestExecutorAttemptAndDeviceExclusion(t *testing.T) {
	exe := setupTestExecutor(t)
	cancelled := false
	require.NoError(t, exe.beginAttempt(1, func(error) { cancelled = true }))
	require.ErrorIs(t, exe.beginAttempt(1, func(error) {}), ErrJobBusy)
	require.ErrorIs(t, exe.Cancel(2), ErrJobNotRunning)
	require.NoError(t, exe.Cancel(1))
	require.True(t, cancelled)
	exe.endAttempt(1)

	require.True(t, exe.OccupyDevice("/dev/nst0"))
	require.False(t, exe.OccupyDevice("/dev/nst0"))
	exe.ReleaseDevice("/dev/nst0")
	require.Contains(t, exe.ListAvailableDevices(), "/dev/nst0")
	// A drive the operator did not configure is never offered for inspection or leasing.
	require.False(t, exe.OccupyDevice("/dev/nst9"))
	require.NotContains(t, exe.ListAvailableDevices(), "/dev/nst9")
}

func TestTapeLeaseAndJobBundleRemainOwnedUntilAttemptEnds(t *testing.T) {
	// Model the interval in which Tape finalization is still waiting for the drive to eject.
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)
	require.NoError(t, exe.beginAttempt(job.ID, func(error) {}))
	require.NoError(t, exe.AcquireTapeDevice(context.Background(), job.ID, "/dev/nst0", nil))

	// The active attempt retains both the device and its evidence-bearing Job bundle.
	require.Empty(t, exe.ListAvailableDevices())
	_, err := exe.DeleteJobs(context.Background(), false, job.ID)
	require.ErrorIs(t, err, ErrJobBusy)
	require.FileExists(t, exe.stateDBPath(job.ID))

	// Successful finalization ends the attempt before either resource becomes reusable.
	exe.endAttempt(job.ID)
	require.Equal(t, []string{"/dev/nst0"}, exe.ListAvailableDevices())
	_, err = exe.DeleteJobs(context.Background(), false, job.ID)
	require.NoError(t, err)
	require.NoFileExists(t, exe.stateDBPath(job.ID))
}

func TestCancelArchiveMediaReturnsToReady(t *testing.T) {
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)
	started := make(chan struct{})
	stopped := make(chan struct{})

	err := exe.StartJob(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(ctx context.Context, _ Runner) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	require.NoError(t, err)
	<-started
	require.ErrorIs(t, exe.StartJob(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(context.Context, Runner) error {
		return nil
	}), ErrJobBusy)
	_, err = exe.DeleteJobs(context.Background(), false, job.ID)
	require.ErrorIs(t, err, ErrJobBusy)
	require.NoError(t, exe.Cancel(job.ID))
	<-stopped
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, time.Second, time.Millisecond)
	stored, err := exe.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, stored.Status)
	require.Contains(t, stored.Error, attemptCancelledByOperator)
}

func TestAttemptsFreezeJobSettingsAtAdmission(t *testing.T) {
	// Save one operator value before admitting an attempt that remains active across a later edit.
	ctx := context.Background()
	exe := setupTestExecutor(t)
	job := createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0)
	waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
	stored, err := exe.lib.Settings().Job.Current(ctx)
	require.NoError(t, err)
	stored.Execution.ReadBatch = 4
	_, err = exe.lib.Settings().Job.Save(ctx, stored)
	require.NoError(t, err)
	started := make(chan int32, 1)
	release := make(chan struct{})
	mediaFailure := errors.New("Media operation failed")
	require.NoError(t, exe.StartJob(ctx, job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(runCtx context.Context, _ Runner) error {
		// Observe the admitted clone before holding the attempt across an operator edit.
		settings, err := JobExecutionSettings(runCtx)
		if err != nil {
			return err
		}
		started <- settings.GetReadBatch()
		<-release

		// The same attempt must still expose its original clone after the edit.
		settings, err = JobExecutionSettings(runCtx)
		if err != nil {
			return err
		}
		started <- settings.GetReadBatch()
		return mediaFailure
	}))
	require.EqualValues(t, 4, <-started)

	// Editing Settings cannot retarget the admitted attempt, but its next Media operation observes the new value.
	stored.Execution.ReadBatch = 8
	_, err = exe.lib.Settings().Job.Save(ctx, stored)
	require.NoError(t, err)
	close(release)
	require.EqualValues(t, 4, <-started)
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, time.Second, time.Millisecond)
	require.NoError(t, exe.StartJob(ctx, job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(runCtx context.Context, _ Runner) error {
		settings, err := JobExecutionSettings(runCtx)
		if err != nil {
			return err
		}
		started <- settings.GetReadBatch()
		return nil
	}))
	require.EqualValues(t, 8, <-started)
	waitJobReleased(t, exe, job.ID)
}

func TestIndexFailureSettlesAsFailedPreparation(t *testing.T) {
	// A failed preparation is one durable FAILED state with its reason, and its manifest checkpoint
	// stays where the preparation stopped.
	exe := setupTestExecutor(t)
	job := createTestJob(t, exe, entity.JobKind_JOB_KIND_RESTORE, 0)
	waitJobReleased(t, exe, job.ID)
	stored, err := exe.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, stored.Status)
	require.Equal(t, entity.JobStatus_JOB_STATUS_PREPARING, stored.Checkpoint)
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, stored.Phase)
	require.Contains(t, stored.Error, "injected index failure")
}

func TestColdJobReportsNoPhase(t *testing.T) {
	// A Job nobody is running has no live stage: its durable state is the whole answer, and inventing
	// a stage for it would put two vocabularies on one fact.
	exe := setupTestExecutor(t)
	for _, status := range []entity.JobStatus{
		entity.JobStatus_JOB_STATUS_PREPARING, entity.JobStatus_JOB_STATUS_READY, entity.JobStatus_JOB_STATUS_FAILED, entity.JobStatus_JOB_STATUS_COMPLETED,
	} {
		require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, exe.jobPhase(999), status)
	}
}

func TestRunningJobIDsReturnsSortedSnapshot(t *testing.T) {
	exe := setupTestExecutor(t)
	for _, id := range []int64{7, 2, 5} {
		require.NoError(t, exe.beginAttempt(id, func(error) {}))
	}

	require.Equal(t, []int64{2, 5, 7}, exe.RunningJobIDs())
	exe.endAttempt(5)
	require.Equal(t, []int64{2, 7}, exe.RunningJobIDs())
	ids, ready := exe.TryQuiesce()
	require.False(t, ready)
	require.Equal(t, []int64{2, 7}, ids)
	exe.endAttempt(2)
	exe.endAttempt(7)
	ids, ready = exe.TryQuiesce()
	require.True(t, ready)
	require.Empty(t, ids)
	require.ErrorIs(t, exe.beginAttempt(9, func(error) {}), ErrJobBusy)
}

func TestJobCatalogSchemaExcludesRuntimeSnapshot(t *testing.T) {
	exe := setupTestExecutor(t)

	// The catalog keeps the published columns and indexes under their existing names.
	for _, column := range []string{
		"id", "executor_id", "created_at_ns", "updated_at_ns", "deleted_at_ns", "revision",
		"target_name", "location_id", "media_id", "catalog_kind",
	} {
		require.True(t, exe.db.Migrator().HasColumn(ModelJob, column), column)
	}
	for _, index := range []string{
		"idx_executor_id", "idx_job_updated_at", "idx_job_deleted_at", "idx_job_revision",
		"idx_jobs_location", "idx_jobs_media", "idx_jobs_kind",
	} {
		require.True(t, exe.db.Migrator().HasIndex(ModelJob, index), index)
	}

	// Hydrated bundle state and live runner observations never become catalog columns.
	for _, column := range []string{"error", "kind", "status", "priority", "phase", "checkpoint"} {
		require.False(t, exe.db.Migrator().HasColumn(ModelJob, column), column)
	}
}

func TestJobRevisionsSupportIncrementalPollingAndDeletion(t *testing.T) {
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)

	// Persist every catalog instant in an explicit nanosecond column.
	for _, column := range []string{"created_at_ns", "updated_at_ns", "deleted_at_ns"} {
		require.True(t, exe.db.Migrator().HasColumn(&jobCatalogRow{}, column), column)
	}
	for _, column := range []string{"created_at", "updated_at", "deleted_at", "create_time", "update_time", "delete_time"} {
		require.False(t, exe.db.Migrator().HasColumn(&jobCatalogRow{}, column), column)
	}

	// Touch through GORM's autoUpdateTime callback at a deterministic nanosecond.
	touchedAt := (job.UpdatedAtNS/int64(time.Millisecond)+1)*int64(time.Millisecond) + 123
	previousNow := exe.db.Config.NowFunc
	exe.db.Config.NowFunc = func() time.Time { return time.Unix(0, touchedAt) }
	t.Cleanup(func() { exe.db.Config.NowFunc = previousNow })
	exe.TouchJob(job.ID)
	var touched jobCatalogRow
	require.NoError(t, exe.db.First(&touched, job.ID).Error)
	require.Equal(t, touchedAt, touched.UpdatedAtNS)
	require.Greater(t, touched.Revision, job.Revision)

	cursor := job.Revision
	changed, err := exe.ListJob(context.Background(), &entity.JobFilter{ChangedAfterRevision: &cursor})
	require.NoError(t, err)
	require.Len(t, changed.Jobs, 1)
	require.Zero(t, changed.Jobs[0].DeletedAtNS)
	cursor = changed.Revision

	// The soft_delete plugin advances UpdatedAtNS and keeps a nanosecond tombstone.
	deletedAt := touchedAt + 100
	require.Equal(t, touchedAt/int64(time.Millisecond), deletedAt/int64(time.Millisecond))
	exe.db.Config.NowFunc = func() time.Time { return time.Unix(0, deletedAt) }
	_, err = exe.DeleteJobs(context.Background(), false, job.ID)
	require.NoError(t, err)
	changed, err = exe.ListJob(context.Background(), &entity.JobFilter{ChangedAfterRevision: &cursor})
	require.NoError(t, err)
	require.Len(t, changed.Jobs, 1)
	require.Equal(t, job.ID, changed.Jobs[0].ID)
	require.Equal(t, deletedAt, changed.Jobs[0].UpdatedAtNS)
	require.Equal(t, deletedAt, int64(changed.Jobs[0].DeletedAtNS))
	require.Greater(t, changed.Revision, cursor)
}

func TestJobRevisionAndSnapshotPagination(t *testing.T) {
	exe := setupTestExecutor(t)
	fixedNow := time.Unix(1, 123)
	exe.db.Config.NowFunc = func() time.Time { return fixedNow }

	// Create more same-millisecond changes than one response page can hold.
	for i := 0; i < 5; i++ {
		job := createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0)
		waitJobStatus(t, exe, job.ID, entity.JobStatus_JOB_STATUS_READY)
	}
	limit := int64(2)
	cursor := int64(0)
	found := make(map[int64]struct{})
	for {
		page, err := exe.ListJob(context.Background(), &entity.JobFilter{
			ChangedAfterRevision: &cursor, Limit: &limit,
		})
		require.NoError(t, err)
		for _, job := range page.Jobs {
			found[job.ID] = struct{}{}
		}
		cursor = page.Revision
		if !page.HasMore {
			break
		}
	}
	require.Len(t, found, 5)

	// Keep the initial descending ID scan on one fixed revision snapshot.
	var beforeID *int64
	var snapshot *int64
	found = make(map[int64]struct{})
	for {
		page, err := exe.ListJob(context.Background(), &entity.JobFilter{
			Limit: &limit, BeforeId: beforeID, SnapshotRevision: snapshot,
		})
		require.NoError(t, err)
		for _, job := range page.Jobs {
			found[job.ID] = struct{}{}
		}
		if snapshot == nil {
			snapshot = &page.Revision
		}
		if !page.HasMore {
			break
		}
		next := page.Jobs[len(page.Jobs)-1].ID
		beforeID = &next
	}
	require.Len(t, found, 5)
}

func TestCleanupFailedJobIgnoresCanceledContext(t *testing.T) {
	exe := setupTestExecutor(t)
	job := createTestCatalogJob(t, exe, entity.JobKind_JOB_KIND_UNSPECIFIED)
	require.NoError(t, os.MkdirAll(exe.jobWorkPath(job.ID), 0o755))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exe.cleanupFailedJob(ctx, job.ID)
	var count int64
	require.NoError(t, exe.db.Unscoped().Model(&jobCatalogRow{}).Where("id = ?", job.ID).Count(&count).Error)
	require.Zero(t, count)
	require.NoDirExists(t, exe.jobWorkPath(job.ID))
}

func TestExecutorRefusesLegacySchema(t *testing.T) {
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "executor.db"))
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE jobs (id INTEGER PRIMARY KEY, status INTEGER, priority INTEGER, state BLOB, create_time DATETIME, update_time DATETIME)").Error)

	exe := New(db, &library.Library{}, nil, Paths{Work: t.TempDir()}, Scripts{}, nil)
	require.ErrorContains(t, exe.AutoMigrate(), "yatm-migrate")
}

func TestReconcileStorageHandlesIncompleteAndOrphanBundles(t *testing.T) {
	exe := setupTestExecutor(t)
	emptyCompleted := createTestCatalogJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE)
	require.NoError(t, exe.createBundle(context.Background(), emptyCompleted, &JobRecord{
		ID: singletonJobID, Kind: entity.JobKind_JOB_KIND_ARCHIVE, Status: entity.JobStatus_JOB_STATUS_COMPLETED,
	}, func(db *gorm.DB) error {
		return db.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY)").Error
	}))
	require.NoError(t, exe.ReconcileStorage(context.Background()))
	require.DirExists(t, exe.jobWorkPath(emptyCompleted.ID))

	incomplete := createTestCatalogJob(t, exe, entity.JobKind_JOB_KIND_UNSPECIFIED)
	require.NoError(t, os.MkdirAll(exe.jobWorkPath(incomplete.ID), 0o755))
	require.NoError(t, exe.ReconcileStorage(context.Background()))
	require.NoDirExists(t, exe.jobWorkPath(incomplete.ID))

	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)
	if runner := exe.removeRunner(job.ID); runner != nil {
		require.NoError(t, runner.Close())
	}
	require.NoError(t, exe.db.Unscoped().Delete(&jobCatalogRow{}, job.ID).Error)
	err := exe.ReconcileStorage(context.Background())
	require.ErrorIs(t, err, ErrOrphanBundle)
	require.DirExists(t, exe.jobWorkPath(job.ID))
}

func TestReconcileStorageKeepsScanJobWithEmptyDiff(t *testing.T) {
	exe := setupTestExecutor(t)
	job := createTestCatalogJob(t, exe, entity.JobKind_JOB_KIND_SCAN)
	require.NoError(t, exe.createBundle(context.Background(), job, &JobRecord{
		ID: singletonJobID, Kind: entity.JobKind_JOB_KIND_SCAN, Status: entity.JobStatus_JOB_STATUS_READY,
	}, func(db *gorm.DB) error {
		if err := db.Exec("CREATE TABLE config (id INTEGER PRIMARY KEY)").Error; err != nil {
			return err
		}
		if err := db.Exec("INSERT INTO config (id) VALUES (1)").Error; err != nil {
			return err
		}
		return db.Exec("CREATE TABLE entries (path TEXT PRIMARY KEY)").Error
	}))

	require.NoError(t, exe.ReconcileStorage(context.Background()))
	require.DirExists(t, exe.jobWorkPath(job.ID))
	stored, err := exe.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobKind_JOB_KIND_SCAN, stored.Kind)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, stored.Status)
}
