package restore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func closeTestReadSequence(t *testing.T, source *copyItems) {
	t.Helper()
	t.Cleanup(func() {
		if source.sequence != nil {
			require.NoError(t, source.sequence.Close())
		}
	})
}

func TestRestoreReadSequenceDoesNotDependOnSubmissionBatch(t *testing.T) {
	for _, batch := range []int{1, 2, 8} {
		t.Run(strconv.Itoa(batch), func(t *testing.T) {
			// Every execution page consumes the same complete selected-Media plan.
			runner, copies, session := restoreOrderFixture(t, "a", "b", "c", "d", "e")
			require.NoError(t, runOrderedRestore(t, context.Background(), runner, copies, session, batch))
			require.Equal(t, []string{"e", "d", "c", "b", "a"}, session.opened)
			require.Equal(t, 1, session.observations)
		})
	}
}

func TestRestoreReadSequencePlansOnlyTheSelectedMediaWithItsOwnCursor(t *testing.T) {
	// One Job spans two cartridges; each newly prepared Session owns a separate starting cursor.
	runner, copies, first := restoreOrderFixture(t, "a", "b", "c", "d")
	firstID, secondID := copies[0].MediaID, copies[0].MediaID+1
	require.NoError(t, runner.db.Model(&Copy{}).Where("media_path IN ?", []string{"b", "d"}).
		Update("media_id", secondID).Error)
	second := &orderedRestoreSession{objects: first.objects, available: true, cursor: 200}

	for _, attempt := range []struct {
		id      int64
		session *orderedRestoreSession
		want    []string
	}{
		{firstID, first, []string{"c", "a"}},
		{secondID, second, []string{"b", "d"}},
	} {
		source := &copyItems{runner: runner, mediaID: attempt.id, session: attempt.session, batch: 1}
		closeTestReadSequence(t, source)
		var paths []string
		for {
			items, err := source.nextPage(t.Context())
			if errors.Is(err, io.EOF) {
				break
			}
			require.NoError(t, err)
			require.Len(t, items, 1)
			copy := items[0].(*copyJob).copy
			require.Equal(t, attempt.id, copy.MediaID)
			paths = append(paths, copy.MediaPath)
		}
		require.Equal(t, attempt.want, paths)
		require.Equal(t, 1, attempt.session.observations)
		require.Empty(t, attempt.session.opened)
	}
	require.Equal(t, 1, first.observations, "a later cartridge cannot reuse or query the prior Session")
}

func TestRestoreLogicalPagesKeepStorageOrderWithoutExecutionIndex(t *testing.T) {
	// No access-cost provider means the existing StorageOrder/path/ID keyset is authoritative.
	runner, copies, session := restoreOrderFixture(t, "a", "b")
	session.noProvider = true
	require.NoError(t, runner.db.Model(&Copy{}).Where("media_path = ?", "a").
		Update("storage_order", []byte{2}).Error)
	source := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: 1}

	// Streaming the ordinary order neither creates a temporary sequence nor enters a source.
	var paths []string
	for {
		items, err := source.nextPage(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		require.Len(t, items, 1)
		paths = append(paths, items[0].(*copyJob).copy.MediaPath)
		require.Nil(t, source.sequence)
	}
	require.Equal(t, []string{"b", "a"}, paths)
	require.Zero(t, session.observations)
	require.Empty(t, session.opened)
}

func TestRestoreReadSequenceLargeHandoffKeepsDatabaseJoinsBounded(t *testing.T) {
	for _, noProvider := range []bool{false, true} {
		t.Run(map[bool]string{false: "planned", true: "logical"}[noProvider], func(t *testing.T) {
			// A requested handoff larger than the optimizer window must not become one large SQL IN list.
			const count = 1100
			runner, copies, session := restoreOrderFixture(t, "0000")
			session.available, session.noProvider = false, noProvider
			var baseCopy Copy
			var baseFile File
			require.NoError(t, runner.db.First(&baseCopy, copies[0].ID).Error)
			require.NoError(t, runner.db.First(&baseFile, copies[0].ItemID).Error)
			rows := make([]Copy, 0, count-1)
			files := make([]File, 0, count-1)
			for i := 1; i < count; i++ {
				copy, file := baseCopy, baseFile
				copy.ID, copy.ItemID = baseCopy.ID+int64(i), baseCopy.ItemID+int64(i)
				copy.MediaPath = fmt.Sprintf("%04d", i)
				file.ItemID, file.Path = copy.ItemID, fmt.Sprintf("%s-%04d", baseFile.Path, i)
				rows, files = append(rows, copy), append(files, file)
			}
			require.NoError(t, runner.db.CreateInBatches(files, batchSize).Error)
			require.NoError(t, runner.db.CreateInBatches(rows, batchSize).Error)
			source := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: 2048}
			closeTestReadSequence(t, source)
			maxParameters := 0
			require.NoError(t, runner.db.Callback().Query().After("gorm:query").Register("test:read-join-bound", func(tx *gorm.DB) {
				if tx.Statement.Table == "copies" || tx.Statement.Table == "files" {
					maxParameters = max(maxParameters, len(tx.Statement.Vars))
				}
			}))
			t.Cleanup(func() { _ = runner.db.Callback().Query().Remove("test:read-join-bound") })

			// The outer page still fills in order across bounded Copy and File joins, with no dropped ranks.
			items, err := source.nextPage(context.Background())
			require.NoError(t, err)
			require.Len(t, items, count)
			for i, item := range items {
				require.Equal(t, baseCopy.ID+int64(i), item.(*copyJob).copy.ID)
			}
			require.LessOrEqual(t, maxParameters, batchSize+2)
			_, err = source.nextPage(context.Background())
			require.ErrorIs(t, err, io.EOF)
			require.Empty(t, session.opened)
		})
	}
}

func TestRestoreReadSequenceSmallSelectionDoesNotReserveLargeHandoff(t *testing.T) {
	for _, noProvider := range []bool{false, true} {
		t.Run(map[bool]string{false: "planned", true: "logical"}[noProvider], func(t *testing.T) {
			// A large configured maximum must not allocate slots for nonexistent work.
			runner, copies, session := restoreOrderFixture(t, "a", "b")
			session.available, session.noProvider = false, noProvider
			source := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: 1 << 20}
			closeTestReadSequence(t, source)

			// A small selection finishes in one handoff while its allocation stays at the SQL window bound.
			items, err := source.nextPage(t.Context())
			require.NoError(t, err)
			require.Len(t, items, 2)
			require.LessOrEqual(t, cap(items), batchSize)
			_, err = source.nextPage(t.Context())
			require.ErrorIs(t, err, io.EOF)
		})
	}
}

func TestRestoreReadSequenceRefillsSkippedRanksWithoutLosingItems(t *testing.T) {
	// A verified output skips the first rank, leaving room in the first submission page.
	runner, copies, session := restoreOrderFixture(t, "a", "b", "c", "d")
	session.available = false
	for _, copy := range copies {
		if copy.MediaPath != "a" {
			continue
		}
		path := runner.restoreTarget(copy.TargetPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, []byte("a"), 0600))
	}
	source := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: 2}
	closeTestReadSequence(t, source)

	// Refilling that partial page must not consume and drop the following page's final rank.
	var paths []string
	for {
		items, err := source.nextPage(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		for _, item := range items {
			paths = append(paths, item.(*copyJob).copy.MediaPath)
		}
	}
	require.Equal(t, []string{"b", "c", "d"}, paths)
	require.Empty(t, session.opened, "planning and resolution never enter a physical source")
}

func TestRestoreReadSequenceOmitsVerifiedOutputsFromPredictedRoute(t *testing.T) {
	// Including reused A's long extent would choose A,C,B. Without its nonexistent movement,
	// the actual cheaper route is B,C; a complete plan must use only files needing a read.
	runner, copies, session := restoreOrderFixture(t, "a", "b", "c")
	session.objects = map[string]uint64{"a": 10, "b": 20, "c": 990}
	session.ends = map[string]uint64{"a": 1000}
	for _, copy := range copies {
		if copy.MediaPath == "a" {
			target := runner.restoreTarget(copy.TargetPath)
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
			require.NoError(t, os.WriteFile(target, []byte("a"), 0600))
		}
	}
	require.NoError(t, runOrderedRestore(t, t.Context(), runner, copies, session, 1))
	require.Equal(t, []string{"b", "c"}, session.opened)
	require.Equal(t, 1, session.observations)
}

func TestRestoreReadSequenceAllReusedDoesNotObserveTape(t *testing.T) {
	// Local reuse still succeeds when no Tape motion is needed and a cursor query would fail.
	runner, copies, session := restoreOrderFixture(t, "a", "b")
	session.currentErr = errors.New("cursor unavailable")
	for _, copy := range copies {
		target := runner.restoreTarget(copy.TargetPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
		require.NoError(t, os.WriteFile(target, []byte(copy.MediaPath), 0600))
	}
	require.NoError(t, runOrderedRestore(t, t.Context(), runner, copies, session, 1))
	require.Empty(t, session.opened)
	require.Zero(t, session.observations)
}

func TestRestoreReadSequenceSkipsCompletedRanksAndReplansPending(t *testing.T) {
	// Freeze an execution sequence, then model an earlier result settling the next candidate.
	runner, copies, session := restoreOrderFixture(t, "a", "b", "c")
	session.available = false
	source := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: 1}
	closeTestReadSequence(t, source)
	items, err := source.nextPage(context.Background())
	require.NoError(t, err)
	require.Equal(t, "a", items[0].(*copyJob).copy.MediaPath)
	require.NoError(t, runner.db.Model(&Copy{}).Where("media_path = ?", "b").
		Update("status", entity.CopyStatus_COPY_STATUS_COMPLETED).Error)

	// An empty pending page does not hide later ranks, and repeated EOF does not reopen the index.
	items, err = source.nextPage(context.Background())
	require.NoError(t, err)
	require.Equal(t, "c", items[0].(*copyJob).copy.MediaPath)
	for range 2 {
		items, err = source.nextPage(context.Background())
		require.ErrorIs(t, err, io.EOF)
		require.Empty(t, items)
	}

	// A new explicit attempt regenerates from pending candidates rather than replaying old ranks.
	require.NoError(t, runner.db.Model(&Copy{}).Where("media_path = ?", "a").
		Update("status", entity.CopyStatus_COPY_STATUS_COMPLETED).Error)
	replanned := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: 2}
	closeTestReadSequence(t, replanned)
	items, err = replanned.nextPage(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "c", items[0].(*copyJob).copy.MediaPath)
	_, err = replanned.nextPage(context.Background())
	require.ErrorIs(t, err, io.EOF)
}

func TestRestoreReadSequenceFeedsAcrossBlockedSourceClose(t *testing.T) {
	for _, noProvider := range []bool{false, true} {
		t.Run(strconv.FormatBool(noProvider), func(t *testing.T) {
			// Gate the first actual Close after its payload has finished, retaining ACP's physical lane.
			runner, copies, session := restoreOrderFixture(t, "a", "b")
			session.noProvider = noProvider
			first, second := "a", "b"
			if !noProvider {
				first, second = "b", "a"
			}
			gate := make(chan struct{})
			release := sync.OnceFunc(func() { close(gate) })
			session.closePath, session.closing, session.closeRelease = first, make(chan struct{}), gate
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t.Cleanup(cancel)
			source := &copyItems{runner: runner, mediaID: copies[0].MediaID, session: session, batch: 1}
			closeTestReadSequence(t, source)
			buffer := startTestBuffer(t, runner)
			engine, err := acp.NewStream(ctx, buffer.onResults, acp.WithHashPolicy(acp.HashReadRefresh),
				acp.Overwrite(false), acp.SetFromDevice(media.DeviceOptions(media.AccessSequential)...),
				acp.WithResultBatch(100), acp.WithResultBuffer(100), acp.WithResultFlushInterval(time.Hour))
			require.NoError(t, err)
			t.Cleanup(func() { release(); _ = engine.Close() })
			items, err := source.nextPage(ctx)
			require.NoError(t, err)
			require.NoError(t, engine.Submit(items...))
			select {
			case <-session.closing:
			case <-ctx.Done():
				t.Fatal("first source did not reach Close")
			}

			// A second page is available while Close is blocked, without an ACP fatal notification or result flush.
			items, err = source.nextPage(ctx)
			require.NoError(t, err)
			require.Len(t, items, 1)
			require.Equal(t, second, items[0].(*copyJob).copy.MediaPath)
			require.NoError(t, engine.Submit(items...))
			session.lock.Lock()
			opened := append([]string(nil), session.opened...)
			session.lock.Unlock()
			require.Equal(t, []string{first}, opened, "ACP still serializes source access through actual Close")

			// The ordinary drain joins Close, then reports and stages both complete results.
			done := make(chan error, 1)
			go func() { done <- errors.Join(source.consume(ctx, engine), buffer.Close()) }()
			select {
			case err := <-done:
				t.Fatalf("Restore drain returned before source Close settled: %v", err)
			default:
			}
			release()
			require.NoError(t, <-done)
			require.Equal(t, []string{first, second}, session.opened)
			require.Equal(t, 2, session.closes)
		})
	}
}
