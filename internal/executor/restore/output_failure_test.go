package restore

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRestoreUnreadableExistingOutputDoesNotStopIndependentItems(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		ready        bool
		writeFailure bool
	}{
		{name: "existing"},
		{name: "staged", ready: true},
		{name: "recording-fails", writeFailure: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			// Freeze two independent outputs, then make the first one unreadable for this attempt.
			ctx := context.Background()
			runner, _, copies := setupVolumeRestore(t, scenario.name, map[string][]byte{
				"a-blocked.txt": []byte("first"), "b-readable.txt": []byte("second"),
			})
			var blocked, readable *copyCandidate
			for _, copy := range copies {
				if filepath.Base(copy.MediaPath) == "a-blocked.txt" {
					blocked = copy
				} else {
					readable = copy
				}
			}
			require.NotNil(t, blocked)
			require.NotNil(t, readable)
			filename := runner.restoreTarget(blocked.TargetPath)
			require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
			require.NoError(t, os.WriteFile(filename, []byte("first"), 0o600))
			if scenario.ready {
				require.NoError(t, runner.stageOutput(ctx, blocked, blocked.Hash, blocked.Size, false))
			}
			require.NoError(t, os.Chmod(filename, 0))
			t.Cleanup(func() { require.NoError(t, os.Chmod(filename, 0o600)) })
			if file, err := os.Open(filename); err == nil {
				require.NoError(t, file.Close())
				t.Skip("the current identity can read files without read permission")
			} else {
				require.ErrorIs(t, err, os.ErrPermission)
			}

			// Losing the ability to record the item still makes the whole attempt fail.
			writeErr := errors.New("cannot record Restore item failure")
			if scenario.writeFailure {
				require.NoError(t, runner.db.Callback().Update().Before("gorm:update").Register("test:restore-item-failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "files" {
						tx.AddError(writeErr)
					}
				}))
				t.Cleanup(func() { _ = runner.db.Callback().Update().Remove("test:restore-item-failure") })
			}
			source := &copyItems{runner: runner, mediaID: blocked.MediaID, batch: 1,
				session: &testReadSession{root: "/media", capabilities: mediapkg.Capabilities{Read: mediapkg.AccessConcurrentRandom}}}
			items, err := source.nextPage(ctx)
			if scenario.writeFailure {
				require.ErrorIs(t, err, writeErr)
				require.Empty(t, items)
				return
			}

			// The bad output stays pending with its explanation while the next item is submitted once.
			require.NoError(t, err)
			require.Len(t, items, 1)
			require.Equal(t, readable.ItemID, items[0].(*copyJob).copy.ItemID)
			require.ErrorIs(t, source.failure, os.ErrPermission)
			var output File
			require.NoError(t, runner.db.First(&output, blocked.ItemID).Error)
			require.Contains(t, output.ResultMessage, "permission denied")
			require.Equal(t, scenario.ready, output.Ready)
			var stored Copy
			require.NoError(t, runner.db.First(&stored, blocked.ID).Error)
			require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, stored.Status)
			items, err = source.nextPage(ctx)
			require.ErrorIs(t, err, io.EOF)
			require.Empty(t, items)
		})
	}
}

func TestRestoreOutputVerificationKeepsAttemptFailuresFatal(t *testing.T) {
	// Neither an unsafe output path nor cancellation is a per-file read failure.
	runner, _, copies := setupVolumeRestore(t, "verification-boundary", map[string][]byte{"file.txt": []byte("saved")})
	copy := copies[0]
	_, _, err := runner.outputMatches(context.Background(), "../outside.txt", copy.Hash, copy.Size)
	require.Error(t, err)
	var local *restoreItemError
	require.False(t, errors.As(err, &local))

	// A stopped attempt must not turn into a pending item and continue the feed.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = runner.outputMatches(ctx, copy.TargetPath, copy.Hash, copy.Size)
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, errors.As(err, &local))
}
