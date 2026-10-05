package restore

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestRestoreTapeWaitDoesNotProbeAnotherAttemptDevice(t *testing.T) {
	// A fake probe records physical access without touching a real Tape drive.
	ctx := context.Background()
	base, lib := setupTestExecutor(t)
	root := filepath.Dir(base.Paths().Work)
	marker := filepath.Join(root, "probed")
	script := filepath.Join(root, "read-info")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n: > '"+marker+"'\n"+
		"printf '%s' '{\"barcode\":\"\"}' > \"$OUT\"\n"), 0o755))
	db, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	exe := executor.New(db, lib, []string{"/dev/nst0"}, base.Paths(), executor.Scripts{ReadInfo: script}, nil)
	require.NoError(t, exe.AutoMigrate())
	_, file := createMediaFile(t, lib, newTapeMedia("ABC001"), "", "file.txt", []byte("saved"), nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	t.Cleanup(func() {
		if exe.IsRunning(job.ID) {
			_ = exe.Cancel(job.ID)
			require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
		}
		_ = value.Close()
	})
	release, err := exe.AcquireJobResource(ctx, "tape:/dev/nst0", nil)
	require.NoError(t, err)
	defer func() { release() }()
	api := &service{exe: exe}
	request := &entity.RestoreMediaRequest{Id: job.ID, Target: (&entity.ReadTapeTarget{Device: "/dev/nst0"}).Pack()}

	// The held drive must queue the attempt before even its cartridge identity is probed.
	_, err = api.RestoreMedia(ctx, request)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		stored, err := exe.GetJob(ctx, job.ID)
		return err == nil && stored.Phase == entity.JobPhase_JOB_PHASE_QUEUED
	}, time.Second, time.Millisecond)
	require.NoFileExists(t, marker)
	require.NoError(t, exe.Cancel(job.ID))
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
	failed, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, "Cancelled by the operator", failed.Error)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, failed.Checkpoint)
	require.NoFileExists(t, marker)

	// A later attempt probes only after handover and releases the lease even on identity failure.
	_, err = api.RestoreMedia(ctx, request)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		stored, err := exe.GetJob(ctx, job.ID)
		return err == nil && stored.Phase == entity.JobPhase_JOB_PHASE_QUEUED
	}, time.Second, time.Millisecond)
	release()
	release = func() {}
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
	require.FileExists(t, marker)
	failed, err = exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Contains(t, failed.Error, "Restore Media identity is unavailable")
	require.Equal(t, entity.JobPhase_JOB_PHASE_UNSPECIFIED, failed.Phase)
	require.Equal(t, []string{"/dev/nst0"}, exe.ListAvailableDevices())

	// A valid identity reaches Session setup using the same lease; no real encryption script runs.
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf x >> '"+marker+"'\n"+
		"printf '%s' '{\"barcode\":\"ABC001\"}' > \"$OUT\"\n"), 0o755))
	_, err = api.RestoreMedia(ctx, request)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
	failed, err = exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Contains(t, failed.Error, "configure Tape encryption failed")
	probes, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "xx", string(probes), "identity resolution and Session setup both own the drive")
	require.Equal(t, []string{"/dev/nst0"}, exe.ListAvailableDevices())
}
