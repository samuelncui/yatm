package archive

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// archiveStorageDevice supplies only in-memory record settlement, never a native Tape descriptor.
type archiveStorageDevice struct {
	issued, settle chan struct{}
	writes, marks  atomic.Int32
	writeErr       error
	acceptedError  bool
	markErr        error
}

func (d *archiveStorageDevice) Write(_ context.Context, data []byte) (tape.RecordResult, error) {
	// Hold one submitted WRITE so the test can inspect staging before physical settlement.
	if d.writes.Add(1) == 1 && d.issued != nil {
		close(d.issued)
		<-d.settle
	}

	// Distinguish an unknown command result from a fully accepted command carrying cancellation.
	if d.writeErr != nil && !d.acceptedError {
		return tape.RecordResult{}, d.writeErr
	}
	return tape.RecordResult{AcceptedKnown: true, Bytes: len(data)}, d.writeErr
}

func (d *archiveStorageDevice) Filemarks(context.Context, uint32) error {
	d.marks.Add(1)
	return d.markErr
}

func (*archiveStorageDevice) Read(context.Context, []byte) (tape.RecordResult, error) {
	return tape.RecordResult{}, errors.New("unexpected fixture READ")
}

func (*archiveStorageDevice) Rewind(context.Context) error {
	return errors.New("unexpected fixture REWIND")
}
func (*archiveStorageDevice) Space(context.Context, byte, int32) error {
	return errors.New("unexpected fixture SPACE")
}
func (*archiveStorageDevice) Sync(context.Context) error { return nil }

func archiveStorageDescriptor() *mediapkg.Descriptor {
	return &mediapkg.Descriptor{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "STG001", CreateTime: time.Unix(1, 0),
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatTarPAXV1, Tar: &entity.TarMediaProfile{
			InitializationId: "11111111-1111-4111-8111-111111111111",
			RecordMode:       entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 512 << 10,
		}}).Pack()}
}

func TestArchiveTarStagedStorageWaitsForPhysicalCompletion(t *testing.T) {
	for _, failure := range []string{"none", "unknown WRITE", "accepted cancellation", "filemark", "cleanup"} {
		t.Run(failure, func(t *testing.T) {
			// Capture ordinary inputs, then make one original unavailable before ACP opens it.
			ctx := context.Background()
			exe := setupTestExecutor(t, executor.Scripts{})
			for _, name := range []string{"a-first", "b-missing", "c-last"} {
				require.NoError(t, os.WriteFile(filepath.Join(exe.Paths().Source, name), []byte(name), 0600))
			}
			job := createArchiveJob(t, exe, &archiveTestSource{Base: exe.Paths().Source, Path: []string{"."}})
			waitIndexed(t, exe, job.ID)
			value, err := exe.GetJobRunner(ctx, job.ID)
			require.NoError(t, err)
			runner := value.(*jobArchiveRunner)
			require.NoError(t, os.Remove(filepath.Join(exe.Paths().Source, "b-missing")))

			// Inject the physical completion outcome without a native device or command retry.
			device := &archiveStorageDevice{issued: make(chan struct{}), settle: make(chan struct{})}
			var settle sync.Once
			release := func() { settle.Do(func() { close(device.settle) }) }
			t.Cleanup(release)
			cause := errors.New("injected " + failure)
			if failure == "unknown WRITE" {
				device.writeErr = &tape.UncertainError{Cause: cause}
			}
			if failure == "accepted cancellation" {
				cause = context.Canceled
				device.writeErr, device.acceptedError = cause, true
			}
			if failure == "filemark" {
				device.markErr = cause
			}
			cleanups := atomic.Int32{}
			session, err := mediapkg.NewTarWriteSession(device, mediapkg.TarSessionOptions{
				Media: archiveStorageDescriptor(), Mode: tape.RecordMode{MaximumBytes: 512 << 10}, TapeFile: 1,
				Cleanup: func(context.Context) error {
					cleanups.Add(1)
					if failure == "cleanup" {
						return cause
					}
					return nil
				},
			})
			require.NoError(t, err)
			target := (&entity.ArchiveTapeTarget{Device: "/dev/nst0", Barcode: "STG001",
				Mode: entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT}).Pack()
			finished := make(chan error, 1)
			require.NoError(t, exe.StartJob(ctx, job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(ctx context.Context, _ executor.Runner) error {
				if err := runner.transition(archiveStatePreparingMedia); err != nil {
					finished <- err
					return err
				}
				err := runner.archivePreparedMedia(ctx, target, session)
				finished <- err
				return err
			}))

			// ACP and its result writer have drained, but the first packed record has not been accepted yet.
			select {
			case <-device.issued:
			case err := <-finished:
				t.Fatalf("Archive finished before physical settlement: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("Archive did not reach its packed final WRITE")
			}
			var items []*Item
			require.NoError(t, runner.db.Order("target_path").Find(&items).Error)
			require.Len(t, items, 3)
			for _, index := range []int{0, 2} {
				require.Equal(t, entity.CopyStatus_COPY_STATUS_STAGED, items[index].Status)
				require.NotNil(t, items[index].Result.Storage)
				require.Nil(t, items[index].MediaID)
			}
			require.Zero(t, items[0].Result.Storage.Metadata.GetTar().HeaderOffsetBytes)
			require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, items[1].Status)
			require.Nil(t, items[1].Result)
			media, err := exe.Lib().ListMedia(ctx, nil)
			require.NoError(t, err)
			require.Empty(t, media, "provisional item positions cannot publish Catalog copies")
			require.Zero(t, device.marks.Load())

			// Only the ordinary finalization/reconciliation path may turn the two successful items into copies.
			release()
			select {
			case err = <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("Archive failed to settle")
			}
			require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
			require.EqualValues(t, 1, cleanups.Load())
			require.NoError(t, runner.db.Order("target_path").Find(&items).Error)
			media, queryErr := exe.Lib().ListMedia(ctx, nil)
			require.NoError(t, queryErr)
			if failure != "none" {
				require.ErrorIs(t, err, cause)
				if failure == "unknown WRITE" || failure == "accepted cancellation" {
					require.Zero(t, device.marks.Load(), "an interrupted WRITE must not trigger a rescue filemark")
				}
				stored, queryErr := exe.GetJob(ctx, job.ID)
				require.NoError(t, queryErr)
				require.Equal(t, entity.JobStatus_JOB_STATUS_READY, stored.Status, "the existing Media-selection lifecycle is unchanged")
				require.ErrorIs(t, err, mediapkg.ErrFinalizeUnusable)
				require.Empty(t, media)
				for _, item := range items {
					require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, item.Status)
					require.Nil(t, item.Result)
				}
				return
			}
			require.NoError(t, err)
			require.Len(t, media, 1)
			positions, err := exe.Lib().ListMediaFilePositions(ctx, media[0].ID, "", 100)
			require.NoError(t, err)
			require.Len(t, positions, 2)
			for _, index := range []int{0, 2} {
				require.Equal(t, entity.CopyStatus_COPY_STATUS_SUBMITTED, items[index].Status)
				require.Nil(t, items[index].Result)
			}
			require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, items[1].Status)
			require.EqualValues(t, 1, device.writes.Load(), "small members remain packed into one physical record")
			require.NoError(t, filepath.WalkDir(exe.Paths().Work, func(path string, entry os.DirEntry, err error) error {
				require.NoError(t, err)
				require.NotContains(t, entry.Name(), "tar-completed", path)
				return nil
			}))
		})
	}
}

func TestArchiveTarStorageUsesExistingBatchAndFinalizationPass(t *testing.T) {
	// Real ACP results include tar positions before the existing writer persists its configured batches.
	ctx, root := context.Background(), t.TempDir()
	var items []*Item
	for i := range 9 {
		name := fmt.Sprintf("file-%d", i)
		path := filepath.Join(root, name)
		require.NoError(t, os.WriteFile(path, bytes.Repeat([]byte{byte(i)}, i), 0600))
		items = append(items, &Item{ID: int64(i + 1), Status: entity.CopyStatus_COPY_STATUS_PENDING, Size: int64(i),
			TargetPath: name, Data: &entity.ArchiveManifestFile{SourcePath: path, Expected: &entity.ExpectedFile{SizeBytes: int64(i)}}})
	}
	runner := newItemTestRunner(t, items...)
	device := &archiveStorageDevice{}
	session, err := mediapkg.NewTarWriteSession(device, mediapkg.TarSessionOptions{Media: archiveStorageDescriptor(),
		Mode: tape.RecordMode{MaximumBytes: 512 << 10}, TapeFile: 1, Cleanup: func(context.Context) error { return nil }})
	require.NoError(t, err)
	reporter := startTestReporter(t, runner)
	var lock sync.Mutex
	var batches []int
	reporter.store = func(ctx context.Context, batch []itemOutcome) error {
		for _, outcome := range batch {
			require.NotNil(t, outcome.copyResult.Storage)
		}
		counter := &countingLogger{Interface: runner.db.Logger}
		copy := &jobArchiveRunner{db: runner.db.Session(&gorm.Session{Logger: counter})}
		err := copy.flushOutcomes(ctx, batch)
		require.Equal(t, len(batch), counter.count(), "storage joins each existing STAGED update without another query")
		lock.Lock()
		batches = append(batches, len(batch))
		lock.Unlock()
		return err
	}
	engine, err := acp.NewStream(ctx, reporter.onResults, acp.WithHashPolicy(acp.HashRead), acp.WithResultBatch(4),
		acp.WithResultFlushInterval(time.Hour), acp.SetToDevice(acp.LinearDevice(true)))
	require.NoError(t, err)
	require.NoError(t, (&copySource{runner: runner, session: session, pageSize: 3}).consume(ctx, engine))
	require.NoError(t, reporter.Close())
	sort.Ints(batches)
	require.Equal(t, []int{1, 4, 4}, batches)
	require.Zero(t, device.writes.Load())

	// The original paged path update also filters/corrects storage; no separate inventory lookup remains.
	result, err := session.Finalize(ctx, nil)
	require.NoError(t, err)
	require.Nil(t, result.Inventory)
	counter := &countingLogger{Interface: runner.db.Logger}
	runner.db = runner.db.Session(&gorm.Session{Logger: counter})
	require.NoError(t, runner.reconcileWriteResult(ctx, result))
	require.Equal(t, len(items)+2, counter.count(), "one row update each plus the page and end-of-page queries")
	for _, item := range items {
		stored := runnerItem(t, runner, item.ID)
		require.Equal(t, entity.CopyStatus_COPY_STATUS_STAGED, stored.Status)
		require.NotNil(t, stored.Result.Storage)
	}
}

func TestArchiveRestartDiscardsProvisionalTarStorage(t *testing.T) {
	// Persist exactly the provisional result a successful member can leave before physical completion.
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	require.NoError(t, os.WriteFile(filepath.Join(exe.Paths().Source, "file"), []byte("content"), 0600))
	job := createArchiveJob(t, exe, &archiveTestSource{Base: exe.Paths().Source, Path: []string{"file"}})
	waitIndexed(t, exe, job.ID)
	db, err := exe.NewStateDB(ctx, job.ID)
	require.NoError(t, err)
	storage := &entity.StoragePosition{Metadata: (&entity.TarMetadata{TapeFile: 1}).Pack()}
	storage.Order = entity.TarStorageOrder(storage.Metadata.GetTar())
	require.NoError(t, db.Model(&Item{}).Where("id > 0").Updates(map[string]any{
		"status": entity.CopyStatus_COPY_STATUS_STAGED, "media_path": "file",
		"result": &entity.ArchiveCopyResult{SizeBytes: 7, Sha256: make([]byte, 32), Storage: storage},
	}).Error)
	connection, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, connection.Close())

	// Reconstruct the actual runner as startup does; the saved position is not restart publication evidence.
	restarted, err := newRunner(ctx, exe, job)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, restarted.Close()) })
	var item Item
	require.NoError(t, restarted.(*jobArchiveRunner).db.First(&item).Error)
	require.Equal(t, entity.CopyStatus_COPY_STATUS_PENDING, item.Status)
	require.Nil(t, item.Result)
	require.Nil(t, item.MediaID)
	media, err := exe.Lib().ListMedia(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, media)
}

func TestArchiveCopyPreservesPageAndStagingFailures(t *testing.T) {
	for _, failPage := range []bool{false, true} {
		t.Run(fmt.Sprint(failPage), func(t *testing.T) {
			// A real ACP result reaches the asynchronous STAGED writer while the feeder requests its next page.
			path := filepath.Join(t.TempDir(), "source")
			require.NoError(t, os.WriteFile(path, []byte("content"), 0600))
			runner := newItemTestRunner(t, &Item{ID: 1, Status: entity.CopyStatus_COPY_STATUS_PENDING,
				Size: 7, TargetPath: "file", Data: &entity.ArchiveManifestFile{SourcePath: path,
					Expected: &entity.ExpectedFile{SizeBytes: 7}}})
			runner.progress = executor.NewProgress()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = executor.WithJobExecutionSettings(ctx, &entity.JobExecutionSettings{
				ReadBatch: 1, ReadBufferMax: 1, WriteBufferMax: 1, WriteBatchSize: 1, FlushIntervalMs: 100,
			})
			writeEntered, pageReturned := make(chan struct{}), make(chan struct{})
			pageErr, writeErr := errors.New("next page unavailable"), errors.New("staging transaction failed")
			queries, writes := atomic.Int32{}, atomic.Int32{}
			require.NoError(t, runner.db.Callback().Query().Before("gorm:query").Register("test:page-failure", func(tx *gorm.DB) {
				if tx.Statement.Table != "items" || queries.Add(1) != 2 {
					return
				}
				select {
				case <-writeEntered:
				case <-ctx.Done():
					tx.AddError(ctx.Err())
				}
				if failPage {
					tx.AddError(pageErr)
				}
				close(pageReturned)
			}))
			require.NoError(t, runner.db.Callback().Update().Before("gorm:update").Register("test:stage-failure", func(tx *gorm.DB) {
				if tx.Statement.Table != "items" || writes.Add(1) != 1 {
					return
				}
				close(writeEntered)
				select {
				case <-pageReturned:
				case <-ctx.Done():
					tx.AddError(ctx.Err())
				}
				tx.AddError(writeErr)
			}))
			session, err := mediapkg.NewTarWriteSession(&archiveStorageDevice{}, mediapkg.TarSessionOptions{
				Media: archiveStorageDescriptor(), Mode: tape.RecordMode{MaximumBytes: 512 << 10}, TapeFile: 1,
				Cleanup: func(context.Context) error { return nil },
			})
			require.NoError(t, err)

			// Drain both independent failures instead of allowing the first page error to hide persistence failure.
			err = runner.copyArchiveItems(ctx, session)
			require.NoError(t, ctx.Err())
			require.Equal(t, failPage, errors.Is(err, pageErr))
			require.ErrorIs(t, err, writeErr)
			require.EqualValues(t, 1, writes.Load())

			// A stopped feeder or failed result writer cannot turn staged bytes into an archive.
			result, finalizeErr := session.Finalize(context.Background(), err)
			require.Nil(t, result)
			require.ErrorIs(t, finalizeErr, mediapkg.ErrFinalizeUnusable)
			require.ErrorIs(t, finalizeErr, writeErr)
		})
	}
}
