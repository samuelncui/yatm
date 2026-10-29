package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/executor/archive"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"gorm.io/gorm"
)

func TestJobWaitUsesSettledArchiveFailure(t *testing.T) {
	// Use the real Archive runner and services with independent catalog reads, as in the server.
	ctx := context.Background()
	root := t.TempDir()
	db, err := resource.OpenSQLite(filepath.Join(root, "catalog.db"))
	require.NoError(t, err)
	reader, err := resource.OpenSQLite(filepath.Join(root, "catalog.db"))
	require.NoError(t, err)
	for _, database := range []*gorm.DB{db, reader} {
		connection, err := database.DB()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, connection.Close()) })
	}
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	exe := executor.NewWithCatalog(db, reader, lib, nil,
		executor.Paths{Work: filepath.Join(root, "work"), Access: []executor.AccessRange{{Root: root}}},
		executor.Scripts{}, nil)
	require.NoError(t, exe.AutoMigrate())
	filename := filepath.Join(root, "file.txt")
	require.NoError(t, os.WriteFile(filename, []byte("fixture"), 0o644))
	job, err := exe.CreateJob(ctx, entity.JobKind_JOB_KIND_ARCHIVE, 0, func(db *gorm.DB) error {
		if err := db.AutoMigrate(&archive.Config{}, &archive.Item{}); err != nil {
			return err
		}
		if err := db.Create(&archive.Item{Status: entity.CopyStatus_COPY_STATUS_PENDING, TargetPath: "file.txt", Size: 7,
			Data: &entity.ArchiveManifestFile{SourcePath: filename}}).Error; err != nil {
			return err
		}
		return db.Create(&archive.Config{ID: 1, Spec: &entity.ArchiveJobSpec{}}).Error
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
	runner, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runner.Close()) })
	api := apis.New(lib, exe)
	server, _ := newGRPCWebTestServer(t, func(server *grpc.Server) {
		entity.RegisterJobServiceServer(server, api)
		exe.RegisterJobServices(server)
	}, nil, nil)

	// Pause the real transition to WaitingForMedia before Executor can record its failure.
	entered, release := make(chan struct{}), make(chan struct{})
	var paused atomic.Bool
	var released sync.Once
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:archive-return", func(tx *gorm.DB) {
		if tx.Statement.Table == "jobs" && runner.Phase() == entity.JobPhase_JOB_PHASE_UNSPECIFIED &&
			paused.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
	}))
	t.Cleanup(func() {
		released.Do(func() { close(release) })
		require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
		require.NoError(t, db.Callback().Update().Remove("test:archive-return"))
	})
	id := strconv.FormatInt(job.ID, 10)
	exit, _, stderr := executeTestCLI(server.URL, "", "archive", "write", "volume", id, "--uuid", "missing-volume")
	require.Equal(t, exitSuccess, exit, stderr)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("Archive did not reach its waiting transition")
	}

	// A wait against the real service must remain active while the failure record is unsettled.
	exit, stdout, stderr := executeTestCLI(server.URL, "", "job", "wait", id,
		"--wait-timeout", "250ms", "--poll-interval", "100ms")
	require.Equal(t, exitFailure, exit)
	require.Contains(t, stderr, "deadline_exceeded")
	require.Contains(t, stdout, "JOB_PHASE_PREPARING_MEDIA")

	// After settlement, the same command returns the retained reason and never selects another Media.
	released.Do(func() { close(release) })
	exit, stdout, stderr = executeTestCLI(server.URL, "", "job", "wait", id,
		"--wait-timeout", "5s", "--poll-interval", "100ms")
	require.Equal(t, exitFailure, exit)
	require.Contains(t, stderr, "action_required")
	require.Contains(t, stderr, "missing-volume")
	require.Contains(t, stdout, "JOB_STATUS_READY")
	require.NotContains(t, stderr, "Job requires intervention")
}
