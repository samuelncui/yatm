package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"gorm.io/gorm"
)

type mediaArchiveReport struct {
	MediaID          int64  `json:"media_id"`
	Identity         string `json:"identity"`
	Name             string `json:"name"`
	FileCount        int64  `json:"file_count"`
	Bytes            int64  `json:"bytes"`
	TerminationError string `json:"termination_error,omitempty"`
}

const (
	archiveMediaCheckpointEvent = "archive_media_checkpoint"
	archiveMediaNoSpaceReason   = "no_space"
)

func (a *jobArchiveRunner) archiveMedia(ctx context.Context, target *entity.ArchiveMediaTarget) error {
	// Library-selected transfers retain their File identities and logical targets through publication.
	var config Config
	if err := a.db.WithContext(ctx).First(&config, 1).Error; err != nil {
		return err
	}

	// Discard candidates left by an interrupted earlier attempt before preparing the physical Media.
	if err := a.resetStagedItems(ctx); err != nil {
		return err
	}
	var pending int64
	if err := a.db.WithContext(ctx).Model(&Item{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).Count(&pending).Error; err != nil {
		return fmt.Errorf("count pending Archive items failed, %w", err)
	}
	if pending == 0 {
		return fmt.Errorf("Archive manifest has no pending items")
	}
	session, err := a.exe.NewMediaBackend(a.job.ID, a.logger, a.mediaWait).NewWriteSession(ctx, target)
	if err != nil {
		return err
	}

	// Track this Media attempt independently until a Library commit publishes verified files. The
	// attempt identity is fixed here and shared by the sampling work path and the request path.
	progress := a.getProgress()
	progress.StartSession()
	a.lock.Lock()
	a.stageKey = progress.StagePrefix()
	a.lock.Unlock()
	committed := false
	defer func() {
		if !committed {
			a.dropProgress()
		}
	}()

	// Run the shared ACP stream and always advance to Backend finalization after copying stops.
	copyErr := a.transition(archiveStateCopying)
	if copyErr == nil {
		copyErr = a.copyArchiveItems(ctx, session)
		if transitionErr := a.transition(archiveStateFinalizingMedia); transitionErr != nil {
			copyErr = errors.Join(copyErr, transitionErr)
		}
	}

	// Let the Backend establish the physical result without inheriting the operation deadline.
	cleanupCtx := context.WithoutCancel(ctx)
	noSpaceErr := targetNoSpaceError(copyErr)
	result, finalizeErr := session.Finalize(cleanupCtx, noSpaceErr != nil)
	if errors.Is(finalizeErr, mediapkg.ErrFinalizeUnusable) || result == nil {
		resetErr := a.resetStagedItems(cleanupCtx)
		if result == nil && finalizeErr == nil {
			finalizeErr = errors.Join(mediapkg.ErrFinalizeUnusable, fmt.Errorf("Media write session returned no result"))
		}
		return errors.Join(copyErr, noSpaceErr, finalizeErr, resetErr)
	}
	reconcileErr := a.reconcileWriteResult(cleanupCtx, result)
	if errors.Is(reconcileErr, mediapkg.ErrFinalizeUnusable) {
		return errors.Join(copyErr, noSpaceErr, finalizeErr, reconcileErr)
	}
	terminationErr := errors.Join(copyErr, noSpaceErr, finalizeErr, reconcileErr)

	// Publish only the candidates retained by a usable finalization result.
	stored, bytes, files, commitErr := a.commitStagedMedia(cleanupCtx, result.Media)
	if commitErr == nil && files > 0 {
		progress.UpdateSessionCurrent(bytes, files)
		progress.CommitSession()
		committed = true
		a.logArchiveMediaNoSpaceCheckpoint(cleanupCtx, stored.ID, files, bytes, terminationErr)
		if err := a.writeMediaReport(cleanupCtx, stored, files, bytes, terminationErr); err != nil {
			a.logger.WithContext(cleanupCtx).WithError(err).Warnf(
				"write Archive report failed, media_id=%d", stored.ID,
			)
		}
	}
	return errors.Join(terminationErr, commitErr)
}

func (a *jobArchiveRunner) logArchiveMediaNoSpaceCheckpoint(
	ctx context.Context,
	mediaID, files, bytes int64,
	terminationErr error,
) {
	if !errors.Is(terminationErr, mediapkg.ErrTargetNoSpace) {
		return
	}
	a.logger.WithContext(ctx).
		WithError(terminationErr).
		WithField("event", archiveMediaCheckpointEvent).
		WithField("reason", archiveMediaNoSpaceReason).
		WithField("media_id", mediaID).
		WithField("files", files).
		WithField("bytes", bytes).
		Warn("Archive Media checkpoint committed")
}

func (a *jobArchiveRunner) commitStagedMedia(
	ctx context.Context,
	descriptor *mediapkg.Descriptor,
) (*library.Media, int64, int64, error) {
	// Count verified staged items before publishing their Media facts.
	media, err := libraryMedia(descriptor)
	if err != nil {
		return nil, 0, 0, err
	}
	if media == nil {
		return nil, 0, 0, fmt.Errorf("Archive Session returned no Media")
	}
	var files, bytes int64
	query := a.db.WithContext(ctx).Model(&Item{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_STAGED)
	if err := query.Count(&files).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("count staged Archive items failed, %w", err)
	}
	if files == 0 {
		return media, 0, 0, nil
	}
	if err := query.Select("COALESCE(SUM(size), 0)").Scan(&bytes).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("sum staged Archive bytes failed, %w", err)
	}

	// Publish the verified Media and make this Job searchable from its actual destination.
	stored, err := a.exe.Lib().CommitMedia(ctx, media, a.stagedMediaFiles)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("commit Archive Media failed, identity=%q, %w", media.Identity, err)
	}
	if err := a.exe.AddJobProperties(ctx, a.job.ID, executor.JobProperty{Key: executor.JobPropertyMedia, Value: stored.ID}); err != nil {
		return nil, 0, 0, err
	}

	// Advance submitted state only after successful Library publication.
	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		record := new(executor.JobRecord)
		if err := tx.First(record, 1).Error; err != nil {
			return fmt.Errorf("read Archive Job state failed, %w", err)
		}
		if record.Status != entity.JobStatus_JOB_STATUS_READY {
			return fmt.Errorf("Archive commit requires READY Job, status=%s", record.Status)
		}
		updated := tx.Model(&Item{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_STAGED).
			Updates(map[string]any{
				"status": entity.CopyStatus_COPY_STATUS_SUBMITTED, "media_id": stored.ID, "result": nil,
			})
		if updated.Error != nil {
			return fmt.Errorf("submit Archive items failed, %w", updated.Error)
		}
		if updated.RowsAffected != files {
			return fmt.Errorf("submit Archive items failed, affected=%d expected=%d", updated.RowsAffected, files)
		}
		var pending int64
		if err := tx.Model(&Item{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).Count(&pending).Error; err != nil {
			return fmt.Errorf("count pending Archive items failed, %w", err)
		}
		if pending == 0 {
			// Completion moves the manifest position with the state: both describe the same Job.
			if err := tx.Model(&executor.JobRecord{}).Where("id = ?", 1).
				Updates(map[string]any{"status": entity.JobStatus_JOB_STATUS_COMPLETED, "checkpoint": entity.JobStatus_JOB_STATUS_COMPLETED}).Error; err != nil {
				return fmt.Errorf("complete Archive Job failed, %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return stored, bytes, files, nil
}

func (a *jobArchiveRunner) copyArchiveItems(ctx context.Context, session mediapkg.WriteSession) error {
	// One attempt reads its pipeline tunables from the Library settings that own them.
	settings, err := a.copyExecutionSettings(ctx)
	if err != nil {
		return err
	}

	// The results callback only hands outcomes to the writer; the shared writer stages each batch
	// of them outside the feed, so the copy pipeline never waits for a database write.
	reporter, err := newReporter(context.WithoutCancel(ctx), a)
	if err != nil {
		return err
	}
	// The writer is drained on every return path; Close is idempotent, so the run path below reads
	// its terminal error again.
	defer func() { _ = reporter.Close() }()
	source := &copySource{runner: a, session: session, pageSize: settings.page}
	engine, err := acp.NewStream(
		ctx,
		reporter.onResults,
		acp.WithHashPolicy(acp.HashReadRefresh),
		acp.WithReadBuffer(settings.readBuffer),
		acp.WithResultBuffer(settings.resultBuffer),
		acp.WithResultBatch(settings.resultBatch),
		acp.WithResultFlushInterval(settings.resultFlushInterval),
		acp.SetToDevice(mediapkg.DeviceOptions(session.Capabilities().Write)...),
		acp.WithLogger(a.logger),
		acp.WithEventHandler(a.archiveEventHandler(ctx)),
	)
	if err != nil {
		return err
	}
	copyErr := source.consume(ctx, engine)

	// A result no durable row can express does not stop the feed, so the attempt reports it here;
	// a failed staging write is the writer's terminal error and is reported the same way.
	writeErr := reporter.Close()
	if copyErr == nil {
		copyErr = writeErr
	}
	if copyErr == nil {
		copyErr = reporter.err()
	}
	if copyErr == nil {
		return nil
	}
	return fmt.Errorf("stream Archive copy failed, %w", copyErr)
}

// copyExecutionSettings resolves the Job pipeline tunables that ACP and the result writer use.
// Every setting maps onto one ACP option: the manifest page size, the read buffer, the result
// queue, the result batch and the result flush interval.
func (a *jobArchiveRunner) copyExecutionSettings(ctx context.Context) (*copySettings, error) {
	settings, err := executor.JobExecutionSettings(ctx)
	if err != nil {
		return nil, err
	}
	return copySettingsFrom(settings), nil
}

func copySettingsFrom(settings *entity.JobExecutionSettings) *copySettings {
	return &copySettings{
		page:                int(settings.ReadBatch),
		readBuffer:          int(settings.ReadBufferMax),
		resultBuffer:        int(settings.WriteBufferMax),
		resultBatch:         int(settings.WriteBatchSize),
		resultFlushInterval: time.Duration(settings.FlushIntervalMs) * time.Millisecond,
	}
}

func (a *jobArchiveRunner) stagedMediaFiles(ctx context.Context, yield func(*library.MediaFile) error) error {
	// This source is consumed only after Media Finalize has admitted the staged results for publication.
	var cursor string
	for {
		// Keep the verified transfer manifest bounded while preserving actual Media path order.
		var items []*Item
		if err := a.db.WithContext(ctx).Where("status = ? AND media_path > ?", entity.CopyStatus_COPY_STATUS_STAGED, cursor).
			Order("media_path").Limit(batchSize).Find(&items).Error; err != nil {
			return fmt.Errorf("query staged Archive items failed, cursor=%q, %w", cursor, err)
		}
		if len(items) == 0 {
			return nil
		}

		// Transferred bytes were verified, but this is not an independent read-back integrity check.
		for _, item := range items {
			if item.MediaPath == "" || item.Result == nil {
				return fmt.Errorf("staged Archive item is incomplete, id=%d", item.ID)
			}
			file := &library.MediaFile{
				Path: item.MediaPath, Size: item.Result.SizeBytes, Mode: fs.FileMode(item.Result.Mode),
				ModTime: time.Unix(0, item.Result.ModTimeNs), WriteTime: time.Unix(0, item.Result.WriteTimeNs),
				Hash: item.Result.Sha256, Expected: item.Data.GetExpected(),
			}
			if item.Result.Storage != nil {
				file.StorageOrder = item.Result.Storage.Order
				file.StorageMetadata = item.Result.Storage.Metadata
			}
			if err := yield(file); err != nil {
				return err
			}
			cursor = item.MediaPath
		}
	}
}

func (a *jobArchiveRunner) resetStagedItems(ctx context.Context) error {
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Item{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_STAGED).
			Updates(map[string]any{
				"status": entity.CopyStatus_COPY_STATUS_PENDING, "media_path": "", "media_id": nil, "result": nil,
			}).Error; err != nil {
			return fmt.Errorf("reset staged Archive items failed, %w", err)
		}
		if err := tx.Model(&Item{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).
			Updates(map[string]any{"media_path": "", "media_id": nil, "result": nil}).Error; err != nil {
			return fmt.Errorf("clear pending Archive results failed, %w", err)
		}
		return nil
	})
}

func (a *jobArchiveRunner) archiveEventHandler(ctx context.Context) acp.EventHandler {
	return func(event acp.Event) {
		switch value := event.(type) {
		case *acp.EventUpdateCount:
			if value.Finished {
				a.logger.WithContext(ctx).Infof("archive copy indexed, files=%d bytes=%d", value.Files, value.Bytes)
			}
		case *acp.EventUpdateProgress:
			a.getProgress().UpdateSessionCurrent(value.Bytes, value.Files)
			// The event that advances the work counters also samples the copy stage, so the estimate
			// is produced by work rather than by a client polling.
			a.sampleStage(a.Phase())
		case *acp.EventReportError:
			a.logger.WithContext(ctx).Errorf(
				"archive copy error, source=%q target=%q error=%q",
				value.Error.Src, value.Error.Dst, value.Error.Err,
			)
		}
	}
}

func (a *jobArchiveRunner) writeMediaReport(
	ctx context.Context,
	media *library.Media,
	files, bytes int64,
	terminationErr error,
) error {
	if media.Kind != entity.MediaKind_MEDIA_KIND_TAPE {
		return nil
	}
	report := &mediaArchiveReport{
		MediaID: media.ID, Identity: media.Identity, Name: media.Name, FileCount: files, Bytes: bytes,
	}
	if terminationErr != nil {
		report.TerminationError = terminationErr.Error()
	}
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return a.exe.WriteReport(ctx, a.job.ID, media.Identity, data)
}
