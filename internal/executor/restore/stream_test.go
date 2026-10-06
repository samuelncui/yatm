package restore

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// setupVolumeRestore publishes one Volume holding the given files and freezes one Restore
// Job over every saved version of them.
func setupVolumeRestore(t *testing.T, name string, files map[string][]byte) (*jobRestoreRunner, *entity.ReadVolumeTarget, []*copyCandidate) {
	t.Helper()
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	volumeRoot := filepath.Join(exe.Paths().Volumes[0], name)
	require.NoError(t, os.MkdirAll(volumeRoot, 0o755))
	volume, err := mediapkg.InitializeVolume(volumeRoot, &entity.VolumeMediaProfile{
		SerialNumber: name, Type: entity.VolumeType_VOLUME_TYPE_HDD,
	})
	require.NoError(t, err)

	// Publish every source file into the Library under this Volume.
	media := &library.Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: name,
		Profile: volume.Marker.Profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS}
	fileIDs := make([]int64, 0, len(files))
	for filename, content := range files {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(volumeRoot, filename)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(volumeRoot, filename), content, 0o644))
		_, file := createMediaFile(t, lib, media, "", filename, content, nil)
		fileIDs = append(fileIDs, file.ID)
	}
	job := createRestoreJob(t, exe, fileIDs...)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)

	var copies []*Copy
	require.NoError(t, runner.db.Order("id").Find(&copies).Error)
	require.Len(t, copies, len(files))
	candidates, err := runner.copyCandidates(ctx, copies)
	require.NoError(t, err)
	return runner, &entity.ReadVolumeTarget{Uuid: volume.Marker.UUID}, candidates
}

// completeCandidate reports one ACP result through the attempt's results callback, drains the
// writer, and returns the attempt's terminal error.
func completeCandidate(t *testing.T, runner *jobRestoreRunner, copy *copyCandidate, result acp.Result) error {
	t.Helper()
	buffer := startTestBuffer(t, runner, copy.MediaID, &testReadSession{root: "/media"})
	item := &copyJob{source: "/media/" + copy.MediaPath, target: runner.restoreTarget(copy.TargetPath), copy: copy}
	result.Job = item
	if err := buffer.onResults([]acp.Result{result}); err != nil {
		return err
	}
	return buffer.Close()
}

// startTestBuffer opens the result path of a Media attempt that is not running, with fixed
// execution settings, and drains its writer when the test ends.
func startTestBuffer(t *testing.T, runner *jobRestoreRunner, mediaID int64, session mediapkg.ReadSession) *copyBuffer {
	t.Helper()
	buffer, err := newCopyBuffer(context.Background(), runner, mediaID, session)
	require.NoError(t, err)
	t.Cleanup(func() { _ = buffer.Close() })
	return buffer
}

// TestRestoreTargetFailureIsPerItemOutcome maps ACP's per-target outcome onto the Job: one
// failed target is no longer an item ACP could not process, so the candidate keeps its
// PENDING status, records the read outcome, and reports the target error with its identity.
func TestRestoreTargetFailureIsPerItemOutcome(t *testing.T) {
	runner, _, copies := setupVolumeRestore(t, "partial", map[string][]byte{"file.txt": []byte("saved")})
	copy := copies[0]

	err := completeCandidate(t, runner, copy, acp.Result{Size: copy.Size, SHA256: copy.Hash,
		Targets: []acp.TargetResult{{Path: runner.restoreTarget(copy.TargetPath), Err: acp.ErrTargetNoSpace}}})
	require.ErrorIs(t, err, acp.ErrTargetNoSpace)
	require.ErrorContains(t, err, "restore target failed")

	var stored Copy
	require.NoError(t, runner.db.First(&stored, copy.ID).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, stored.Status)
	var output File
	require.NoError(t, runner.db.First(&output, copy.ItemID).Error)
	require.False(t, output.Ready, "a failed target stages no output")
}

func TestRestoreItemFailureDoesNotStopLaterResultBatches(t *testing.T) {
	runner, _, copies := setupVolumeRestore(t, "independent", map[string][]byte{
		"first.txt": []byte("first"), "second.txt": []byte("second"),
	})
	buffer := startTestBuffer(t, runner, copies[0].MediaID, &testReadSession{root: "/media"})
	result := func(copy *copyCandidate, err error) acp.Result {
		return acp.Result{Job: &copyJob{source: "/media/" + copy.MediaPath, target: runner.restoreTarget(copy.TargetPath), copy: copy},
			Size: copy.Size, SHA256: copy.Hash,
			Targets: []acp.TargetResult{{Path: runner.restoreTarget(copy.TargetPath), Size: copy.Size, Err: err}}}
	}

	// Wait for the first failure to settle before delivering a later independent completion.
	require.NoError(t, buffer.onResults([]acp.Result{result(copies[0], acp.ErrTargetNoSpace)}))
	require.Eventually(t, func() bool {
		var output File
		return runner.db.First(&output, copies[0].ItemID).Error == nil && output.ResultMessage != ""
	}, 5*time.Second, time.Millisecond)
	require.NoError(t, buffer.onResults([]acp.Result{result(copies[1], nil)}))
	require.ErrorIs(t, buffer.Close(), acp.ErrTargetNoSpace)
	var output File
	require.NoError(t, runner.db.First(&output, copies[1].ItemID).Error)
	require.True(t, output.Ready, "a settled item failure must not reject later accepted results")
}

func TestRestoreReusedReadyOutputRetainsDamage(t *testing.T) {
	ctx := context.Background()
	runner, _, copies := setupVolumeRestore(t, "damaged-reuse", map[string][]byte{"file.txt": []byte("saved")})
	copy := copies[0]
	content := []byte("damaged output")
	hash := sha256.Sum256(content)
	require.NoError(t, os.MkdirAll(filepath.Dir(runner.restoreTarget(copy.TargetPath)), 0o755))
	require.NoError(t, os.WriteFile(runner.restoreTarget(copy.TargetPath), content, 0o644))
	require.NoError(t, runner.stageOutput(ctx, copy, hash[:], int64(len(content)), true))

	// A later explicit Media attempt verifies and reuses these bytes without promoting them.
	source := &copyItems{runner: runner, mediaID: copy.MediaID, session: &testReadSession{root: "/media"}, batch: 1}
	item, err := source.resolve(ctx, copy)
	require.NoError(t, err)
	require.Nil(t, item)
	var output File
	require.NoError(t, runner.db.First(&output, copy.ItemID).Error)
	require.True(t, output.Ready)
	require.True(t, output.Damaged)
	require.Equal(t, hash[:], output.ActualHash)
}

// TestRestoreEveryTargetFailedIsStillACompletion keeps the completion path reachable for an
// item that wrote nothing anywhere: the runner resolves it, reports it, and leaves the
// candidate pending for a later attempt.
func TestRestoreEveryTargetFailedIsStillACompletion(t *testing.T) {
	runner, _, copies := setupVolumeRestore(t, "nowhere", map[string][]byte{"file.txt": []byte("saved")})
	copy := copies[0]

	err := completeCandidate(t, runner, copy, acp.Result{Size: copy.Size, SHA256: copy.Hash,
		Targets: []acp.TargetResult{{Path: runner.restoreTarget(copy.TargetPath), Err: errors.New("destination is read-only")}}})
	require.ErrorContains(t, err, "destination is read-only")

	// A target error is never a damage finding: the source read is unverified, not unhealthy.
	var stored Copy
	require.NoError(t, runner.db.First(&stored, copy.ID).Error)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
}

// TestRestoreResultsArePersistedByTheSharedWriter proves the writer is the only persistence path:
// the callback only enqueues, and the drain records the staged facts it accepted.
func TestRestoreResultsArePersistedByTheSharedWriter(t *testing.T) {
	runner, _, copies := setupVolumeRestore(t, "callback", map[string][]byte{"file.txt": []byte("saved")})
	copy := copies[0]

	var statements atomic.Int64
	count := func(*gorm.DB) { statements.Add(1) }
	require.NoError(t, runner.db.Callback().Query().Before("gorm:query").Register("test:callback-query-io", count))
	require.NoError(t, runner.db.Callback().Create().Before("gorm:create").Register("test:callback-create-io", count))
	t.Cleanup(func() {
		_ = runner.db.Callback().Query().Remove("test:callback-query-io")
		_ = runner.db.Callback().Create().Remove("test:callback-create-io")
	})

	err := completeCandidate(t, runner, copy, acp.Result{Size: copy.Size, SHA256: copy.Hash,
		Targets: []acp.TargetResult{{Path: runner.restoreTarget(copy.TargetPath), Size: copy.Size}}})
	require.NoError(t, err)
	require.NotZero(t, statements.Load(), "the drain persists its own results")

	var output File
	require.NoError(t, runner.db.First(&output, copy.ItemID).Error)
	require.True(t, output.Ready)
}

// TestRestoreWriterFailureStopsTheAttempt pins the mapping for a failed persistence: the error is
// reported by the writer, which stops the feed, and the candidate stays PENDING.
func TestRestoreWriterFailureStopsTheAttempt(t *testing.T) {
	// Prepare one pending copy shared by both failure cases.
	runner, _, copies := setupVolumeRestore(t, "failure", map[string][]byte{"file.txt": []byte("saved")})
	copy := copies[0]

	// A target-level failure is an item outcome, not a staged result.
	buffer := startTestBuffer(t, runner, copy.MediaID, &testReadSession{root: "/media"})
	item := &copyJob{source: "/media/" + copy.MediaPath, target: runner.restoreTarget(copy.TargetPath), copy: copy}
	require.NoError(t, buffer.onResults([]acp.Result{{
		Job: item, Size: copy.Size, SHA256: copy.Hash,
		Targets: []acp.TargetResult{{Path: runner.restoreTarget(copy.TargetPath), Err: acp.ErrTargetNoSpace}},
	}}))
	require.ErrorIs(t, buffer.Close(), acp.ErrTargetNoSpace)
	var output File
	require.NoError(t, runner.db.First(&output, copy.ItemID).Error)
	require.False(t, output.Ready)

	// A failed persistence is the runner's own failure and is reported the same way.
	persistErr := errors.New("job database is unavailable")
	require.NoError(t, runner.db.Callback().Update().Before("gorm:update").Register("test:restore-persist", func(tx *gorm.DB) {
		if tx.Statement.Table == "copies" {
			tx.AddError(persistErr)
		}
	}))
	t.Cleanup(func() { _ = runner.db.Callback().Update().Remove("test:restore-persist") })
	buffer = startTestBuffer(t, runner, copy.MediaID, &testReadSession{root: "/media"})

	// The asynchronous write may fail before the callback returns or after it hands over the batch.
	err := buffer.onResults([]acp.Result{{
		Job: item, Size: copy.Size, SHA256: copy.Hash,
		Targets: []acp.TargetResult{{Path: runner.restoreTarget(copy.TargetPath), Size: copy.Size}},
	}})
	require.True(t, err == nil || errors.Is(err, persistErr), "callback reported %v", err)

	// Once recorded, the persistence error stops the feed and survives the drain without staging output.
	require.Eventually(t, func() bool { return buffer.writer.Failure() != nil }, 5*time.Second, time.Millisecond)
	require.ErrorIs(t, buffer.onResults(nil), persistErr, "the recorded error stops the feed")
	require.ErrorIs(t, buffer.Close(), persistErr)
	var stored Copy
	require.NoError(t, runner.db.First(&stored, copy.ID).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, stored.Status)
	require.NoError(t, runner.db.First(&output, copy.ItemID).Error)
	require.False(t, output.Ready, "a failed persistence stages nothing")
}

// TestRestorePagesGiveEveryCandidateOneItem proves one manifest page is one Submit: the pager
// hands over the batch it resolved and reports io.EOF when the manifest is exhausted.
func TestRestorePagesGiveEveryCandidateOneItem(t *testing.T) {
	ctx := context.Background()
	runner, _, copies := setupVolumeRestore(t, "handover", map[string][]byte{
		"first.txt": []byte("first"), "second.txt": []byte("second"),
	})

	// One candidate per batch forces one handover between the two items.
	session := &testReadSession{root: "/media", capabilities: mediapkg.Capabilities{Read: mediapkg.AccessConcurrentRandom}}
	source := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: 1}

	items, err := source.nextPage(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.IsType(t, &copyJob{}, items[0])

	items, err = source.nextPage(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1)

	items, err = source.nextPage(ctx)
	require.ErrorIs(t, err, io.EOF)
	require.Empty(t, items)
}

// TestRestoreGracefulStopEndsFeedingWithoutAFailure keeps the operator's own cancellation out of
// the persistence path: the pager hands over no element once the attempt has stopped.
func TestRestoreGracefulStopEndsFeedingWithoutAFailure(t *testing.T) {
	runner, _, copies := setupVolumeRestore(t, "cancel", map[string][]byte{
		"first.txt": []byte("first"), "second.txt": []byte("second"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	session := &testReadSession{root: "/media", capabilities: mediapkg.Capabilities{Read: mediapkg.AccessConcurrentRandom}}
	source := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: 1}
	_, err := source.nextPage(ctx)
	require.NoError(t, err)
	cancel()

	// A stopped pager reports the cancellation instead of producing another page.
	_, err = source.nextPage(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

// TestRestoreAbandonedItemStaysPending pins the mapping for items ACP could not process or
// abandoned during a graceful stop: the candidate keeps its PENDING status and the attempt
// reports the stopping error.
func TestRestoreAbandonedItemStaysPending(t *testing.T) {
	runner, _, copies := setupVolumeRestore(t, "abandoned", map[string][]byte{"file.txt": []byte("saved")})
	copy := copies[0]

	session := &testReadSession{root: "/media"}
	buffer := startTestBuffer(t, runner, copy.MediaID, session)
	item := &copyJob{source: "/media/" + copy.MediaPath, target: runner.restoreTarget(copy.TargetPath), copy: copy}
	require.NoError(t, buffer.onResults([]acp.Result{{Job: item, Err: context.Canceled}}))
	require.ErrorIs(t, buffer.Close(), context.Canceled)

	var stored Copy
	require.NoError(t, runner.db.First(&stored, copy.ID).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, stored.Status)
}
