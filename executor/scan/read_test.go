package scan

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/stretchr/testify/require"
)

func TestVerifyReadPreparationErrorCompletesAsUnreadable(t *testing.T) {
	// Permission errors during ACP preparation do not produce a sink result but remain per-file findings.
	exe, volume, media := setupVerifyExecutor(t)
	position := saveVerifyFile(t, exe, volume, media, "restricted", []byte("content"))
	saveVerifyFile(t, exe, volume, media, "z-readable", []byte("saved"))
	filename := filepath.Join(volume.Root, position.Path)
	require.NoError(t, os.Chmod(filename, 0))
	t.Cleanup(func() { _ = os.Chmod(filename, 0644) })
	opened, err := os.Open(filename)
	if err == nil {
		require.NoError(t, opened.Close())
		t.Skip("current user can read files without permission bits")
	}
	id := createVerifyJob(t, exe, media.ID, volume)
	runVerifyVolume(t, exe, id, volume)
	waitVerifyIdle(t, exe, id, entity.JobStatus_COMPLETED)

	// Finalized results retain the independent expected baseline and publish only an unreadable observation.
	stored, err := exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_UNREADABLE, stored.Health)
	require.Equal(t, position.Hash, stored.Hash)
	progress, err := (&service{exe: exe}).GetProgress(context.Background(), &entity.GetScanJobProgressRequest{Id: id})
	require.NoError(t, err)
	require.EqualValues(t, 1, progress.Unreadable)
	require.EqualValues(t, 1, progress.Matched)
	require.Zero(t, progress.Progress.TotalFiles-progress.Matched-progress.Unreadable)
}

type sequentialCheckSession struct{ checkSession }

func (*sequentialCheckSession) Capabilities() mediapkg.Capabilities {
	return mediapkg.Capabilities{Read: mediapkg.AccessSequential}
}

func TestVerifySequentialManifestCursorUsesStorageOrder(t *testing.T) {
	ctx := context.Background()
	exe, volume, media := setupVerifyExecutor(t)
	var expected []*library.Position
	for index := 0; index < batchSize+5; index++ {
		position := saveVerifyFile(t, exe, volume, media, fmt.Sprintf("file-%04d", index), nil)
		position.StorageOrder = make([]byte, 8)
		binary.BigEndian.PutUint64(position.StorageOrder, uint64((batchSize+6-index)/2))
		require.NoError(t, exe.Lib().SavePosition(ctx, position))
		expected = append(expected, position)
	}
	sort.Slice(expected, func(i, j int) bool {
		first, second := binary.BigEndian.Uint64(expected[i].StorageOrder), binary.BigEndian.Uint64(expected[j].StorageOrder)
		if first != second {
			return first < second
		}
		return expected[i].Path < expected[j].Path
	})
	id := createVerifyJob(t, exe, media.ID, volume)
	value, err := exe.GetJobRunner(ctx, id)
	require.NoError(t, err)
	r := value.(*runner)
	var config Config
	require.NoError(t, r.db.First(&config, 1).Error)
	session := &sequentialCheckSession{checkSession{root: volume.Root, media: media}}
	require.NoError(t, r.enumerateMedia(ctx, &config, session))
	stream := &contentStream{runner: r, config: &config, scope: &Scope{}, session: session}
	for _, position := range expected {
		request, err := stream.Next(ctx)
		require.NoError(t, err)
		require.Equal(t, filepath.Join(volume.Root, position.Path), request.Source)
	}
	_, err = stream.Next(ctx)
	require.ErrorIs(t, err, io.EOF)
}

func TestVerifyCancellationFinalizesWithoutInventingFindings(t *testing.T) {
	// A canceled attempt must release its physical Session while leaving unseen inventory untouched.
	exe, volume, media := setupVerifyExecutor(t)
	position := saveVerifyFile(t, exe, volume, media, "file", []byte("content"))
	id := createVerifyJob(t, exe, media.ID, volume)
	value, err := exe.GetJobRunner(context.Background(), id)
	require.NoError(t, err)
	r := value.(*runner)
	var config Config
	err = r.db.First(&config, 1).Error
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	finalized := 0
	err = r.runPipeline(ctx, &config, &Scope{}, nil, &checkSession{root: volume.Root, media: media, finalize: func(ctx context.Context) error {
		finalized++
		return ctx.Err()
	}})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, finalized)

	// Cancellation is a Job lifecycle event, not a missing or unreadable observation for each remaining file.
	stored, err := exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
	var entry Entry
	require.NoError(t, r.db.First(&entry, position.ID).Error)
	require.Equal(t, entity.ScanFinding_NOT_CHECKED, entry.Finding)
	require.False(t, entry.Published)
}

type failedDeviceSession struct{ checkSession }

func (*failedDeviceSession) SourcePath(name string) (string, error) {
	return "", &os.PathError{Op: "stat", Path: name, Err: syscall.EIO}
}

func TestVerifyDeviceFailureKeepsRemainingEntriesUnchecked(t *testing.T) {
	// A device-level failure is not an independent unreadable observation of every remaining path.
	exe, volume, media := setupVerifyExecutor(t)
	position := saveVerifyFile(t, exe, volume, media, "file", []byte("content"))
	id := createVerifyJob(t, exe, media.ID, volume)
	value, err := exe.GetJobRunner(context.Background(), id)
	require.NoError(t, err)
	r := value.(*runner)
	var config Config
	err = r.db.First(&config, 1).Error
	require.NoError(t, err)
	finalized := 0
	err = r.runPipeline(context.Background(), &config, &Scope{}, nil, &failedDeviceSession{checkSession{media: media,
		finalize: func(context.Context) error { finalized++; return nil }}})
	require.ErrorIs(t, err, syscall.EIO)
	require.Equal(t, 1, finalized)

	// Finalization releases resources, but neither Job findings nor Library health are fabricated.
	var entry Entry
	require.NoError(t, r.db.First(&entry, position.ID).Error)
	require.Equal(t, entity.ScanFinding_NOT_CHECKED, entry.Finding)
	stored, err := exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
}

func TestVerifyByteProgressDoesNotRequireCompletedFile(t *testing.T) {
	progress := executor.NewProgress()
	progress.SetGlobalCopied(100, 1)
	stream := &contentStream{runner: &runner{progress: progress}, processed: 0}
	handler := stream.eventHandler(context.Background())
	handler(&acp.EventUpdateProgress{Bytes: 50})
	require.EqualValues(t, 150, progress.ToEntity().CopiedBytes)
	require.EqualValues(t, 1, progress.ToEntity().CopiedFiles)
}
