package executor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestFailedJobCleanupReportsRemovalFailure(t *testing.T) {
	// A bundle that cannot be removed does not stop the remaining catalog removals.
	if os.Geteuid() == 0 {
		t.Skip("permission fixture requires an unprivileged user")
	}
	exe := setupTestExecutor(t)
	job := createTestCatalogJob(t, exe, entity.JobKind_JOB_KIND_UNSPECIFIED)
	path := exe.jobWorkPath(job.ID)
	require.NoError(t, os.MkdirAll(path, 0700))
	parent := filepath.Dir(path)
	t.Cleanup(func() { require.NoError(t, os.Chmod(parent, 0700)) })
	require.NoError(t, os.Chmod(parent, 0500))

	// The caller receives the bundle failure after the catalog entry is removed.
	err := exe.cleanupFailedJob(context.Background(), job.ID)
	require.ErrorIs(t, err, os.ErrPermission)
	var count int64
	require.NoError(t, exe.db.Unscoped().Model(&jobCatalogRow{}).Where("id = ?", job.ID).Count(&count).Error)
	require.Zero(t, count)
	require.DirExists(t, path)

	// A later attempt removes the retained bundle.
	require.NoError(t, os.Chmod(parent, 0700))
	require.NoError(t, exe.cleanupFailedJob(context.Background(), job.ID))
	require.NoDirExists(t, path)
}

type cleanupFailureRunner struct {
	testRunner
	failure error
}

func (r *cleanupFailureRunner) Close() error { return r.failure }

func TestFailedJobCleanupRetainsRunnerOnCloseFailure(t *testing.T) {
	// Retain failed construction resources while their owner reports an unsettled close.
	exe := setupTestExecutor(t)
	job := createTestCatalogJob(t, exe, entity.JobKind_JOB_KIND_UNSPECIFIED)
	failure := errors.New("runner close failed")
	runner := &cleanupFailureRunner{failure: failure}
	exe.runners[job.ID] = runner
	require.NoError(t, os.MkdirAll(exe.jobWorkPath(job.ID), 0700))

	// A successful explicit cleanup can subsequently release the same runner and bundle.
	require.ErrorIs(t, exe.cleanupFailedJob(context.Background(), job.ID), failure)
	require.Same(t, runner, exe.runners[job.ID])
	require.DirExists(t, exe.jobWorkPath(job.ID))
	runner.failure = nil
	require.NoError(t, exe.cleanupFailedJob(context.Background(), job.ID))
	require.NotContains(t, exe.runners, job.ID)
	require.NoDirExists(t, exe.jobWorkPath(job.ID))
}

func TestDeleteJobRetainsRunnerOnCloseFailure(t *testing.T) {
	exe := setupTestExecutor(t)
	job := waitJobStatus(t, exe, createTestJob(t, exe, entity.JobKind_JOB_KIND_ARCHIVE, 0).ID, entity.JobStatus_JOB_STATUS_READY)
	old := exe.removeRunner(job.ID)
	require.NoError(t, old.Close())
	failure := errors.New("owned cleanup failed")
	runner := &cleanupFailureRunner{failure: failure}
	exe.runners[job.ID] = runner
	_, err := exe.DeleteJobs(context.Background(), false, job.ID)
	require.ErrorIs(t, err, failure)
	require.Same(t, runner, exe.runners[job.ID])
	require.FileExists(t, exe.stateDBPath(job.ID))
	runner.failure = nil
	_, err = exe.DeleteJobs(context.Background(), false, job.ID)
	require.NoError(t, err)
	require.NotContains(t, exe.runners, job.ID)
}
