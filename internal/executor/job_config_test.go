package executor

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type creationConfigProbe struct {
	ID     int64 `gorm:"primaryKey"`
	Value  string
	onRead func(*gorm.DB) error
}

func (creationConfigProbe) TableName() string { return "config" }

func (c *creationConfigProbe) AfterFind(db *gorm.DB) error {
	if c.onRead != nil {
		return c.onRead(db)
	}
	return nil
}

func creationConfigJob(t *testing.T, exe *Executor, status entity.JobStatus) *Job {
	t.Helper()
	// Seed retained state directly so the read must not need a cached runner or an attempt.
	job := createTestCatalogJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE)
	require.NoError(t, exe.createBundle(context.Background(), job, &JobRecord{
		ID: 1, Kind: entity.JobKind_JOB_KIND_ARCHIVE, Priority: -17, Status: status,
		Checkpoint: entity.JobStatus_JOB_STATUS_READY, Error: "retained reason",
		LatestAttemptStartedAtNS: new(int64(123)), LatestAttemptFinishedAtNS: new(int64(456)),
	}, func(db *gorm.DB) error {
		if err := db.AutoMigrate(&creationConfigProbe{}); err != nil {
			return err
		}
		return db.Create(&creationConfigProbe{ID: 1, Value: "retained input"}).Error
	}))
	return job
}

func TestReadJobConfigIsReadOnlyInEveryState(t *testing.T) {
	for _, state := range []entity.JobStatus{
		entity.JobStatus_JOB_STATUS_PREPARING, entity.JobStatus_JOB_STATUS_READY,
		entity.JobStatus_JOB_STATUS_COMPLETED, entity.JobStatus_JOB_STATUS_FAILED,
	} {
		t.Run(state.String(), func(t *testing.T) {
			// URI-sensitive characters must still address the existing bundle exactly.
			exe := setupTestExecutor(t)
			t.Cleanup(func() { require.NoError(t, closeGORMDB(exe.db)) })
			job := creationConfigJob(t, exe, state)
			work := filepath.Join(t.TempDir(), "Jobs ?#")
			require.NoError(t, os.Rename(exe.paths.Work, work))
			exe.paths.Work = work
			before, err := os.ReadFile(exe.stateDBPath(job.ID))
			require.NoError(t, err)
			var catalogBefore jobCatalogRow
			require.NoError(t, exe.db.First(&catalogBefore, job.ID).Error)

			// Probe the actual connection: even an attempted write must fail at SQLite.
			for range 3 {
				var connection *sql.DB
				var writeErr error
				config := &creationConfigProbe{onRead: func(db *gorm.DB) error {
					connection, err = db.DB()
					if err != nil {
						return err
					}
					writeErr = db.Exec("UPDATE job SET priority = 99 WHERE id = 1").Error
					return nil
				}}
				priority, err := exe.ReadJobConfig(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE, config)
				require.NoError(t, err)
				require.EqualValues(t, -17, priority)
				require.Equal(t, "retained input", config.Value)
				require.ErrorContains(t, writeErr, "readonly")
				require.Error(t, connection.Ping(), "the read must release its connection")
			}

			// Reads do not initialize runners, start attempts, revise the catalog or modify persisted state.
			after, err := os.ReadFile(exe.stateDBPath(job.ID))
			require.NoError(t, err)
			require.Equal(t, before, after)
			var catalogAfter jobCatalogRow
			require.NoError(t, exe.db.First(&catalogAfter, job.ID).Error)
			require.Equal(t, catalogBefore, catalogAfter)
			require.Empty(t, exe.runners)
			require.Empty(t, exe.attempts)
			require.NoFileExists(t, filepath.Join(exe.jobWorkPath(job.ID), "job.log"))
			require.NoFileExists(t, exe.stateDBPath(job.ID)+"-journal")
		})
	}
}

func TestReadJobConfigDuringActiveAttempt(t *testing.T) {
	// Represent an admitted operation without running a pipeline that could legitimately change the evidence.
	exe := setupTestExecutor(t)
	t.Cleanup(func() { require.NoError(t, closeGORMDB(exe.db)) })
	job := creationConfigJob(t, exe, entity.JobStatus_JOB_STATUS_READY)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	require.NoError(t, exe.beginAttempt(job.ID, cancel))
	defer exe.endAttempt(job.ID)
	before, err := os.ReadFile(exe.stateDBPath(job.ID))
	require.NoError(t, err)

	// Reading creation input neither rejects, cancels nor replaces an existing attempt.
	var config creationConfigProbe
	_, err = exe.ReadJobConfig(ctx, job.ID, entity.JobKind_JOB_KIND_ARCHIVE, &config)
	require.NoError(t, err)
	require.True(t, exe.IsRunning(job.ID))
	require.NoError(t, ctx.Err())
	require.Empty(t, exe.runners)
	after, err := os.ReadFile(exe.stateDBPath(job.ID))
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestReadJobConfigWithRelativeWorkPath(t *testing.T) {
	// The configured work root may be relative to the server's working directory.
	exe := setupTestExecutor(t)
	t.Cleanup(func() { require.NoError(t, closeGORMDB(exe.db)) })
	job := creationConfigJob(t, exe, entity.JobStatus_JOB_STATUS_READY)
	directory, err := os.Getwd()
	require.NoError(t, err)
	exe.paths.Work, err = filepath.Rel(directory, exe.paths.Work)
	require.NoError(t, err)

	// SQLite's read-only URI must still resolve that same bundle without treating the root as a hostname.
	var config creationConfigProbe
	priority, err := exe.ReadJobConfig(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE, &config)
	require.NoError(t, err)
	require.EqualValues(t, -17, priority)
	require.Equal(t, "retained input", config.Value)
}

func TestReadJobConfigExcludesDeletion(t *testing.T) {
	// Hold a typed config read after its SQLite query but before it releases the Job guard.
	exe := setupTestExecutor(t)
	t.Cleanup(func() { require.NoError(t, closeGORMDB(exe.db)) })
	job := creationConfigJob(t, exe, entity.JobStatus_JOB_STATUS_COMPLETED)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	readDone := make(chan error, 1)
	go func() {
		_, err := exe.ReadJobConfig(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE,
			&creationConfigProbe{onRead: func(*gorm.DB) error { close(entered); <-release; return nil }})
		readDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("config read did not reach the guarded query")
	}

	// Deletion uses the same lock and must wait until this reader has closed its connection.
	deleteDone := make(chan error, 1)
	go func() { deleteDone <- exe.deleteJob(context.Background(), job.ID) }()
	require.Eventually(t, func() bool {
		exe.jobLocksLock.Lock()
		defer exe.jobLocksLock.Unlock()
		return exe.jobLocks[job.ID].refs == 2
	}, time.Second, time.Millisecond)
	require.FileExists(t, exe.stateDBPath(job.ID))
	once.Do(func() { close(release) })
	require.NoError(t, <-readDone)
	require.NoError(t, <-deleteDone)
	_, err := exe.ReadJobConfig(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE, &creationConfigProbe{})
	require.ErrorIs(t, err, ErrJobNotFound)
	require.NoDirExists(t, exe.jobWorkPath(job.ID))
}

func TestReadJobConfigRejectsInvalidOrMissingState(t *testing.T) {
	// Admission errors must not manufacture a bundle or inspect a different kind's config.
	exe := setupTestExecutor(t)
	t.Cleanup(func() { require.NoError(t, closeGORMDB(exe.db)) })
	job := creationConfigJob(t, exe, entity.JobStatus_JOB_STATUS_FAILED)
	for _, id := range []int64{0, -1} {
		_, err := exe.ReadJobConfig(context.Background(), id, entity.JobKind_JOB_KIND_ARCHIVE, &creationConfigProbe{})
		require.ErrorContains(t, err, "positive")
	}
	_, err := exe.ReadJobConfig(context.Background(), job.ID+1, entity.JobKind_JOB_KIND_ARCHIVE, &creationConfigProbe{})
	require.ErrorIs(t, err, ErrJobNotFound)
	_, err = exe.ReadJobConfig(context.Background(), job.ID, entity.JobKind_JOB_KIND_SCAN, &creationConfigProbe{})
	require.ErrorContains(t, err, "unexpected job kind")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = exe.ReadJobConfig(ctx, job.ID, entity.JobKind_JOB_KIND_ARCHIVE, &creationConfigProbe{})
	require.ErrorIs(t, err, context.Canceled)

	// A missing database is retained as an error instead of being created or migrated.
	require.NoError(t, os.Remove(exe.stateDBPath(job.ID)))
	_, err = exe.ReadJobConfig(context.Background(), job.ID, entity.JobKind_JOB_KIND_ARCHIVE, &creationConfigProbe{})
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoFileExists(t, exe.stateDBPath(job.ID))
	require.Empty(t, exe.runners)
}
