package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
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

func (r *testRunner) Index(ctx context.Context) error {
	if r.indexErr != nil {
		return r.indexErr
	}
	return r.exe.UpdateJobStatus(ctx, r.job.ID, entity.JobStatus_INDEXING, entity.JobStatus_PENDING)
}

func (r *testRunner) Phase() entity.JobPhase {
	if r.indexErr != nil {
		return entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY
	}
	return entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA
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
	RegisterJobType(entity.JobKind_ARCHIVE, func(_ context.Context, exe *Executor, job *Job) (Runner, error) {
		return &testRunner{exe: exe, job: job}, nil
	}, register)
	RegisterJobType(entity.JobKind_RESTORE, func(_ context.Context, exe *Executor, job *Job) (Runner, error) {
		return &testRunner{exe: exe, job: job, indexErr: errors.New("injected index failure")}, nil
	}, register)
}

func setupTestExecutor(t *testing.T) *Executor {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "executor.db"))
	require.NoError(t, err)
	exe := New(db, &library.Library{}, []string{"/dev/nst0"}, Paths{Work: t.TempDir()}, Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	return exe
}

func createTestJob(t *testing.T, exe *Executor, kind entity.JobKind, priority int64) *Job {
	t.Helper()
	job, err := exe.CreateJob(context.Background(), kind, priority, func(db *gorm.DB) error {
		table := "items"
		if kind == entity.JobKind_RESTORE {
			table = "copies"
		}
		return db.Exec("CREATE TABLE " + table + " (id INTEGER PRIMARY KEY)").Error
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

func TestExecutorJobCatalogAndBundle(t *testing.T) {
	exe := setupTestExecutor(t)
	created := createTestJob(t, exe, entity.JobKind_ARCHIVE, 7)
	created = waitJobStatus(t, exe, created.ID, entity.JobStatus_PENDING)
	require.Equal(t, entity.JobKind_ARCHIVE, created.Kind)
	require.Equal(t, int64(7), created.Priority)
	require.Positive(t, created.CreatedAt)
	require.Positive(t, created.UpdatedAt)

	var catalog Job
	require.NoError(t, exe.db.First(&catalog, created.ID).Error)
	require.Equal(t, localExecutorID, catalog.ExecutorID)
	require.FileExists(t, exe.stateDBPath(created.ID))
	require.DirExists(t, filepath.Join(exe.jobWorkPath(created.ID), "tapes"))
	require.NoDirExists(t, filepath.Join(exe.jobWorkPath(created.ID), "logs"))

	listed, err := exe.ListJob(context.Background(), &entity.JobFilter{})
	require.NoError(t, err)
	require.Len(t, listed.Jobs, 1)
	require.Equal(t, created.ID, listed.Jobs[0].ID)

	require.NoError(t, exe.DeleteJobs(context.Background(), created.ID))
	_, err = exe.GetJob(context.Background(), created.ID)
	require.ErrorIs(t, err, ErrJobNotFound)
	require.NoFileExists(t, exe.stateDBPath(created.ID))
}

func TestExecutorAttemptAndDeviceExclusion(t *testing.T) {
	exe := setupTestExecutor(t)
	cancelled := false
	require.True(t, exe.beginAttempt(1, func() { cancelled = true }))
	require.False(t, exe.beginAttempt(1, func() {}))
	require.ErrorIs(t, exe.Cancel(2), ErrJobNotRunning)
	require.NoError(t, exe.Cancel(1))
	require.True(t, cancelled)
	exe.endAttempt(1)

	require.True(t, exe.OccupyDevice("/dev/nst0"))
	require.False(t, exe.OccupyDevice("/dev/nst0"))
	exe.ReleaseDevice("/dev/nst0")
	require.Contains(t, exe.ListAvailableDevices(), "/dev/nst0")
}

func TestTapeLeaseAndJobBundleRemainOwnedUntilAttemptEnds(t *testing.T) {
	// Model the interval in which Tape finalization is still waiting for the drive to eject.
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_ARCHIVE, 0).ID, entity.JobStatus_PENDING)
	require.True(t, exe.beginAttempt(job.ID, func() {}))
	require.True(t, exe.LeaseTapeDevice(job.ID, "/dev/nst0"))

	// The active attempt retains both the device and its evidence-bearing Job bundle.
	require.Empty(t, exe.ListAvailableDevices())
	require.ErrorIs(t, exe.DeleteJobs(context.Background(), job.ID), ErrJobBusy)
	require.FileExists(t, exe.stateDBPath(job.ID))

	// Successful finalization ends the attempt before either resource becomes reusable.
	exe.endAttempt(job.ID)
	require.Equal(t, []string{"/dev/nst0"}, exe.ListAvailableDevices())
	require.NoError(t, exe.DeleteJobs(context.Background(), job.ID))
	require.NoFileExists(t, exe.stateDBPath(job.ID))
}

func TestStartCancelLeavesJobPending(t *testing.T) {
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_ARCHIVE, 0).ID, entity.JobStatus_PENDING)
	started := make(chan struct{})
	stopped := make(chan struct{})

	err := exe.StartJob(context.Background(), job.ID, entity.JobKind_ARCHIVE, func(ctx context.Context, _ Runner) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	require.NoError(t, err)
	<-started
	require.ErrorIs(t, exe.StartJob(context.Background(), job.ID, entity.JobKind_ARCHIVE, func(context.Context, Runner) error {
		return nil
	}), ErrJobBusy)
	require.ErrorIs(t, exe.DeleteJobs(context.Background(), job.ID), ErrJobBusy)
	require.NoError(t, exe.Cancel(job.ID))
	<-stopped
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, time.Second, time.Millisecond)
	stored, err := exe.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_PENDING, stored.Status)
}

func TestIndexFailureLeavesRetryableJob(t *testing.T) {
	exe := setupTestExecutor(t)
	job := createTestJob(t, exe, entity.JobKind_RESTORE, 0)
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, time.Second, time.Millisecond)

	stored, err := exe.GetJob(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_INDEXING, stored.Status)
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_INDEX_RETRY, stored.Phase)
	require.NoError(t, exe.RetryIndex(context.Background(), job.ID))
}

func TestColdPendingScanWaitsForMedia(t *testing.T) {
	// Tape Scan awaits an explicit physical device; inventory publication has no manual Apply phase.
	exe := setupTestExecutor(t)
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA, exe.jobPhase(
		999, entity.JobKind_SCAN, entity.JobStatus_PENDING,
	))
	require.Equal(t, entity.JobPhase_JOB_PHASE_WAITING_FOR_MEDIA, exe.jobPhase(
		999, entity.JobKind_ARCHIVE, entity.JobStatus_PENDING,
	))
}

func TestRunningJobIDsReturnsSortedSnapshot(t *testing.T) {
	exe := setupTestExecutor(t)
	for _, id := range []int64{7, 2, 5} {
		require.True(t, exe.beginAttempt(id, func() {}))
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
	require.False(t, exe.beginAttempt(9, func() {}))
}

func TestJobRevisionsSupportIncrementalPollingAndDeletion(t *testing.T) {
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_ARCHIVE, 0).ID, entity.JobStatus_PENDING)

	// Keep the catalog on GORM's conventional timestamp column names.
	for _, column := range []string{"created_at", "updated_at", "deleted_at"} {
		require.True(t, exe.db.Migrator().HasColumn(&Job{}, column), column)
	}
	for _, column := range []string{"create_time", "update_time", "delete_time"} {
		require.False(t, exe.db.Migrator().HasColumn(&Job{}, column), column)
	}

	// Touch through GORM's autoUpdateTime callback at a deterministic millisecond.
	touchedAt := job.UpdatedAt + 100
	previousNow := exe.db.Config.NowFunc
	exe.db.Config.NowFunc = func() time.Time { return time.UnixMilli(touchedAt) }
	t.Cleanup(func() { exe.db.Config.NowFunc = previousNow })
	exe.TouchJob(job.ID)
	var touched Job
	require.NoError(t, exe.db.First(&touched, job.ID).Error)
	require.Equal(t, touchedAt, touched.UpdatedAt)
	require.Greater(t, touched.Revision, job.Revision)

	cursor := job.Revision
	changed, err := exe.ListJob(context.Background(), &entity.JobFilter{ChangedAfterRevision: &cursor})
	require.NoError(t, err)
	require.Len(t, changed.Jobs, 1)
	require.Zero(t, changed.Jobs[0].DeletedAt)
	cursor = changed.Revision

	// The soft_delete plugin advances UpdatedAt and keeps a millisecond tombstone.
	deletedAt := touchedAt + 100
	exe.db.Config.NowFunc = func() time.Time { return time.UnixMilli(deletedAt) }
	require.NoError(t, exe.DeleteJobs(context.Background(), job.ID))
	changed, err = exe.ListJob(context.Background(), &entity.JobFilter{ChangedAfterRevision: &cursor})
	require.NoError(t, err)
	require.Len(t, changed.Jobs, 1)
	require.Equal(t, job.ID, changed.Jobs[0].ID)
	require.Equal(t, deletedAt, changed.Jobs[0].UpdatedAt)
	require.Equal(t, deletedAt, int64(changed.Jobs[0].DeletedAt))
	require.Greater(t, changed.Revision, cursor)
}

func TestJobRevisionAndSnapshotPagination(t *testing.T) {
	exe := setupTestExecutor(t)
	fixedNow := time.UnixMilli(1000)
	exe.db.Config.NowFunc = func() time.Time { return fixedNow }

	// Create more same-millisecond changes than one response page can hold.
	for i := 0; i < 5; i++ {
		job := createTestJob(t, exe, entity.JobKind_ARCHIVE, 0)
		waitJobStatus(t, exe, job.ID, entity.JobStatus_PENDING)
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
	job := &Job{ExecutorID: localExecutorID}
	require.NoError(t, exe.db.Create(job).Error)
	require.NoError(t, os.MkdirAll(exe.jobWorkPath(job.ID), 0o755))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exe.cleanupFailedJob(ctx, job.ID)
	var count int64
	require.NoError(t, exe.db.Unscoped().Model(&Job{}).Where("id = ?", job.ID).Count(&count).Error)
	require.Zero(t, count)
	require.NoDirExists(t, exe.jobWorkPath(job.ID))
}

func TestExecutorRefusesLegacySchema(t *testing.T) {
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "executor.db"))
	require.NoError(t, err)
	require.NoError(t, db.Exec("CREATE TABLE jobs (id INTEGER PRIMARY KEY, status INTEGER, priority INTEGER, state BLOB, create_time DATETIME, update_time DATETIME)").Error)

	exe := New(db, &library.Library{}, nil, Paths{Work: t.TempDir()}, Scripts{}, nil)
	require.ErrorContains(t, exe.AutoMigrate(), "offline current migration")
}

func TestReconcileStorageHandlesIncompleteAndOrphanBundles(t *testing.T) {
	exe := setupTestExecutor(t)
	emptyCompleted := &Job{ExecutorID: localExecutorID}
	require.NoError(t, exe.db.Create(emptyCompleted).Error)
	require.NoError(t, exe.createBundle(context.Background(), emptyCompleted, &JobRecord{
		ID: singletonJobID, Kind: entity.JobKind_ARCHIVE, Status: entity.JobStatus_COMPLETED,
	}, func(db *gorm.DB) error {
		return db.Exec("CREATE TABLE items (id INTEGER PRIMARY KEY)").Error
	}))
	require.NoError(t, exe.ReconcileStorage(context.Background()))
	require.DirExists(t, exe.jobWorkPath(emptyCompleted.ID))

	incomplete := &Job{ExecutorID: localExecutorID}
	require.NoError(t, exe.db.Create(incomplete).Error)
	require.NoError(t, os.MkdirAll(exe.jobWorkPath(incomplete.ID), 0o755))
	require.NoError(t, exe.ReconcileStorage(context.Background()))
	require.NoDirExists(t, exe.jobWorkPath(incomplete.ID))

	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_ARCHIVE, 0).ID, entity.JobStatus_PENDING)
	if runner := exe.removeRunner(job.ID); runner != nil {
		require.NoError(t, runner.Close())
	}
	require.NoError(t, exe.db.Unscoped().Delete(&Job{}, job.ID).Error)
	err := exe.ReconcileStorage(context.Background())
	require.ErrorIs(t, err, ErrOrphanBundle)
	require.DirExists(t, exe.jobWorkPath(job.ID))
}

func TestReconcileStorageKeepsScanJobWithEmptyDiff(t *testing.T) {
	exe := setupTestExecutor(t)
	job := &Job{ExecutorID: localExecutorID}
	require.NoError(t, exe.db.Create(job).Error)
	require.NoError(t, exe.createBundle(context.Background(), job, &JobRecord{
		ID: singletonJobID, Kind: entity.JobKind_SCAN, Status: entity.JobStatus_PENDING,
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
	require.Equal(t, entity.JobKind_SCAN, stored.Kind)
	require.Equal(t, entity.JobStatus_PENDING, stored.Status)
}
