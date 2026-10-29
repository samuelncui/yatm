package restore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRestoreMediaPreservesItemAndLateCheckpointFailures(t *testing.T) {
	// One output fails locally while an independent source produces complete bytes.
	runner, target, copies := setupVolumeRestore(t, "joined-errors", map[string][]byte{
		"a-changed.txt": []byte("first"), "b-readable.txt": []byte("second"),
	})
	var changed, readable *copyCandidate
	for _, copy := range copies {
		if filepath.Base(copy.MediaPath) == "a-changed.txt" {
			changed = copy
		} else {
			readable = copy
		}
	}
	require.NotNil(t, changed)
	require.NotNil(t, readable)
	filename := runner.restoreTarget(changed.TargetPath)
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0755))
	require.NoError(t, os.WriteFile(filename, []byte("different bytes"), 0600))

	// Delay the last checkpoint until ACP has returned from its asynchronous result handoff.
	checkpointErr := errors.New("late Restore checkpoint failure")
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		t.Cleanup(unblock)
		require.NoError(t, runner.db.Callback().Create().Before("gorm:create").Register("test:late-checkpoint", func(tx *gorm.DB) {
			output, ok := tx.Statement.Dest.(*File)
			if tx.Statement.Table != "files" || !ok || !output.Ready || output.ItemID != readable.ItemID {
				return
			}
			close(entered)
			<-release
			tx.AddError(checkpointErr)
		}))
		t.Cleanup(func() { _ = runner.db.Callback().Create().Remove("test:late-checkpoint") })
		result := make(chan error, 1)
		require.NoError(t, runner.exe.StartJob(context.Background(), runner.job.ID, entity.JobKind_JOB_KIND_RESTORE,
			func(ctx context.Context, value executor.Runner) error {
				err := value.(*jobRestoreRunner).restore(ctx, &entity.RestoreMediaRequest{Id: runner.job.ID, Target: target.Pack()})
				result <- err
				return err
			}))
		<-entered
		synctest.Wait()
		select {
		case err := <-result:
			t.Fatalf("Media attempt returned before its checkpoint settled: %v", err)
		default:
		}
		unblock()
		err := <-result
		synctest.Wait()

		// The final attempt error must retain both independent causes after physical cleanup.
		var local *restoreItemError
		require.ErrorAs(t, err, &local)
		require.ErrorContains(t, local, "reserved Restore output changed")
		require.ErrorIs(t, err, checkpointErr)
		require.False(t, runner.exe.IsRunning(runner.job.ID))
		require.FileExists(t, runner.restoreTarget(readable.TargetPath))
		var outputs []File
		require.NoError(t, runner.db.Find(&outputs).Error)
		for _, output := range outputs {
			require.False(t, output.Ready)
			require.False(t, output.Completed)
		}
	})
}
