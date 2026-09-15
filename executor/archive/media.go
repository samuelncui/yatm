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
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/tools"
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
	// Library-selected transfers retain their frozen identities through Finalize and publication.
	var config Config
	if err := a.db.WithContext(ctx).First(&config, 1).Error; err != nil {
		return err
	}
	release, err := a.exe.Lib().UseOnlineRead()
	if err != nil {
		return err
	}
	defer release()

	// Discard candidates left by an interrupted earlier attempt before preparing the physical Media.
	if err := a.resetStagedItems(ctx); err != nil {
		return err
	}
	session, err := a.exe.NewMediaBackend(a.job.ID, a.logger).NewWriteSession(ctx, a.db, target)
	if err != nil {
		return err
	}

	// Track this Media attempt independently until a Library commit publishes verified files.
	progress := a.getProgress()
	progress.StartSession()
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

	// Let the Backend establish which staged files are durable without inheriting the operation deadline.
	cleanupCtx := tools.WithoutTimeout(ctx)
	finalizeErr := session.Finalize(cleanupCtx, copyErr)
	if errors.Is(finalizeErr, mediapkg.ErrFinalizeUnusable) {
		return errors.Join(copyErr, finalizeErr)
	}
	terminationErr := errors.Join(copyErr, finalizeErr)

	// Publish only the candidates retained by a usable finalization result.
	stored, bytes, files, commitErr := a.commitStagedMedia(cleanupCtx, session.Inspect())
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
	media *library.Media,
) (*library.Media, int64, int64, error) {
	if media == nil {
		return nil, 0, 0, fmt.Errorf("Archive Session returned no Media")
	}
	var files, bytes int64
	query := a.db.WithContext(ctx).Model(&Item{}).Where("status = ?", entity.CopyStatus_STAGED)
	if err := query.Count(&files).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("count staged Archive items failed, %w", err)
	}
	if files == 0 {
		return media, 0, 0, nil
	}
	if err := query.Select("COALESCE(SUM(size), 0)").Scan(&bytes).Error; err != nil {
		return nil, 0, 0, fmt.Errorf("sum staged Archive bytes failed, %w", err)
	}
	stored, err := a.exe.Lib().CommitMedia(ctx, media, a.stagedMediaFiles)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("commit Archive Media failed, identity=%q, %w", media.Identity, err)
	}
	if err := a.exe.AddJobResources(ctx, a.job.ID, executor.JobResource{Kind: executor.JobResourceMedia, Role: executor.JobResourceDestination, ResourceID: stored.ID}); err != nil {
		return nil, 0, 0, err
	}

	err = a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		record := new(executor.JobRecord)
		if err := tx.First(record, 1).Error; err != nil {
			return fmt.Errorf("read Archive Job state failed, %w", err)
		}
		if record.Status != entity.JobStatus_PENDING {
			return fmt.Errorf("Archive commit requires PENDING Job, status=%s", record.Status)
		}
		updated := tx.Model(&Item{}).Where("status = ?", entity.CopyStatus_STAGED).
			Updates(map[string]any{
				"status": entity.CopyStatus_SUBMITTED, "media_id": stored.ID, "result": nil,
			})
		if updated.Error != nil {
			return fmt.Errorf("submit Archive items failed, %w", updated.Error)
		}
		if updated.RowsAffected != files {
			return fmt.Errorf("submit Archive items failed, affected=%d expected=%d", updated.RowsAffected, files)
		}
		var pending int64
		if err := tx.Model(&Item{}).Where("status = ?", entity.CopyStatus_PENDING).Count(&pending).Error; err != nil {
			return fmt.Errorf("count pending Archive items failed, %w", err)
		}
		if pending == 0 {
			if err := tx.Model(&executor.JobRecord{}).Where("id = ?", 1).
				Update("status", entity.JobStatus_COMPLETED).Error; err != nil {
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
	err := acp.RunStream(
		ctx,
		&copySource{runner: a, session: session},
		&copySink{runner: a},
		acp.WithHash(true),
		acp.WithSignatureCache(true),
		acp.SetToDevice(mediapkg.DeviceOptions(session.Capabilities().Write)...),
		acp.WithLogger(a.logger),
		acp.WithEventHandler(a.archiveEventHandler(ctx)),
	)
	if err != nil {
		return fmt.Errorf("stream Archive copy failed, %w", err)
	}
	return nil
}

func (a *jobArchiveRunner) stagedMediaFiles(ctx context.Context, yield func(*library.MediaFile) error) error {
	// This source is consumed only after Media Finalize has admitted the staged results for publication.
	var cursor string
	for {
		// Keep the verified transfer manifest bounded while preserving actual Media path order.
		var items []*Item
		if err := a.db.WithContext(ctx).Where("status = ? AND media_path > ?", entity.CopyStatus_STAGED, cursor).
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
				Path: item.MediaPath, Size: item.Result.Size, Mode: fs.FileMode(item.Result.Mode),
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
		if err := tx.Model(&Item{}).Where("status = ?", entity.CopyStatus_STAGED).
			Updates(map[string]any{
				"status": entity.CopyStatus_PENDING, "media_path": "", "media_id": nil, "result": nil,
			}).Error; err != nil {
			return fmt.Errorf("reset staged Archive items failed, %w", err)
		}
		if err := tx.Model(&Item{}).Where("status = ?", entity.CopyStatus_PENDING).
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
		case *acp.EventReportError:
			a.logger.WithContext(ctx).Errorf(
				"archive copy error, source=%q target=%q error=%q",
				value.Error.Src, value.Error.Dst, value.Error.Err,
			)
		case *acp.EventUpdateJob:
			// Ignore transient updates and reserve success wording for one completed target.
			if value.Job.Status != acp.JobStatusFinished {
				return
			}
			if len(value.Job.SuccessTargets) == 1 && len(value.Job.FailTargets) == 0 {
				a.logger.WithContext(ctx).Infof(
					"archive file finished, source=%q size=%d", value.Job.FullPath, value.Job.Size,
				)
				return
			}
			reason := "failed"
			if len(value.Job.SuccessTargets) == 0 && len(value.Job.FailTargets) == 0 {
				reason = "skipped"
			}
			// Classify target failures through stable error identity for diagnostic logs.
			for _, err := range value.Job.FailTargets {
				if errors.Is(err, acp.ErrTargetNoSpace) {
					reason = archiveMediaNoSpaceReason
					break
				}
			}
			a.logger.WithContext(ctx).WithField("reason", reason).Warnf(
				"archive file not written, source=%q size=%d", value.Job.FullPath, value.Job.Size,
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
