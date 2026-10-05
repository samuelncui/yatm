//go:build darwin || linux || freebsd

package archive

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
)

func TestArchiveDirectoryChildErrorCannotPrepareReadyJob(t *testing.T) {
	// Real directory enumeration succeeds, but missing search permission makes child metadata unreadable.
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the child-stat permission fixture")
	}
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	root, err := exe.LocationRoot(exe.Paths().Source)
	require.NoError(t, err)
	for _, name := range []string{"first", "second"} {
		require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte("content"), 0644))
	}
	location := &library.Location{Name: "Originals", ExecutorID: "local", RootPath: root}
	require.NoError(t, exe.Lib().CreateLocation(ctx, location))
	t.Cleanup(func() { require.NoError(t, os.Chmod(root, 0755)) })
	require.NoError(t, os.Chmod(root, 0400))
	selections := []*entity.FileSelection{{Target: &entity.FileSelection_Location{
		Location: &entity.LocationSelection{LocationId: location.ID},
	}}}
	api := &service{exe: exe}

	// Estimate fails explicitly; a caller cannot mistake omitted child content for a complete scope.
	estimate, err := api.Estimate(ctx, &entity.EstimateArchiveJobRequest{Selections: selections})
	require.ErrorIs(t, err, os.ErrPermission)
	require.Nil(t, estimate)

	// Creation can expose a preparing Job, but preparation must settle FAILED rather than READY.
	created, err := api.Create(ctx, &entity.CreateArchiveJobRequest{Spec: &entity.ArchiveJobSpec{Selections: selections}})
	require.NoError(t, err)
	var job *executor.Job
	require.Eventually(t, func() bool {
		job, err = exe.GetJob(ctx, created.Job.Id)
		return err == nil && job.Status == entity.JobStatus_JOB_STATUS_FAILED && !exe.IsRunning(job.ID)
	}, 5*time.Second, time.Millisecond)
	require.Contains(t, job.Error, "permission denied")
}
