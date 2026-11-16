package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/hashicorp/go-multierror"
	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"google.golang.org/protobuf/proto"
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

	// Content the chosen registered Media already settles needs no device; settlement completes
	// the Job when nothing remains pending.
	if err := a.settleBeforeMedia(ctx, target); err != nil {
		return err
	}
	if err := a.db.WithContext(ctx).Model(&Item{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).Count(&pending).Error; err != nil {
		return fmt.Errorf("count pending Archive items failed, %w", err)
	}
	if pending == 0 {
		return nil
	}
	session, err := a.exe.OpenWriteSession(ctx, a.job.ID, a.logger, a.mediaWait, target)
	if err != nil {
		return err
	}

	return a.archivePreparedMedia(ctx, target, session)
}

// settleBeforeMedia applies the write-time skip rules to every pending known-content item before an
// APPEND choice opens its registered Media. FORMAT is left alone: a new Media holds nothing yet.
func (a *jobArchiveRunner) settleBeforeMedia(ctx context.Context, target *entity.ArchiveMediaTarget) error {
	var config Config
	if err := a.db.WithContext(ctx).First(&config, 1).Error; err != nil {
		return fmt.Errorf("read Archive config failed, %w", err)
	}
	if config.Spec.GetTargetMediaCount() == 0 {
		return nil
	}
	mediaID, err := a.appendMediaID(ctx, target)
	if err != nil || mediaID == 0 {
		return err
	}

	// Collect the pending known content in bounded pages; settlement reads its Catalog counts.
	var pending []skippedItem
	var cursor int64
	for {
		var items []*Item
		if err := a.db.WithContext(ctx).Select("id", "data").Where("status = ? AND id > ?", entity.CopyStatus_COPY_STATUS_PENDING, cursor).
			Order("id").Limit(batchSize).Find(&items).Error; err != nil {
			return fmt.Errorf("query pending Archive items failed, %w", err)
		}
		if len(items) == 0 {
			return a.settleSkipped(ctx, mediaID, pending)
		}
		for _, item := range items {
			cursor = item.ID
			if signature := item.Data.GetExpected().GetSignature(); len(signature) > 0 {
				pending = append(pending, skippedItem{id: item.ID, signature: signature})
			}
		}
	}
}

// appendMediaID returns the registered Library Media an APPEND choice writes to, or zero for FORMAT
// and for an identity the Library does not hold; opening the Media reports invalid choices.
func (a *jobArchiveRunner) appendMediaID(ctx context.Context, target *entity.ArchiveMediaTarget) (int64, error) {
	var media *library.Media
	var err error
	switch {
	case target.GetTape() != nil:
		if target.GetTape().GetMode() != entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_APPEND {
			return 0, nil
		}
		media, err = a.exe.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_TAPE, target.GetTape().GetBarcode())
	case target.GetVolume() != nil:
		uuid, normalizeErr := mediapkg.NormalizeVolumeUUID(target.GetVolume().GetUuid())
		if normalizeErr != nil {
			return 0, nil
		}
		media, err = a.exe.Lib().GetMediaByIdentity(ctx, entity.MediaKind_MEDIA_KIND_VOLUME, uuid)
	}
	if err != nil || media == nil {
		return 0, err
	}
	return media.ID, nil
}

// archivePreparedMedia owns content execution, finalization and publication for one prepared Session.
func (a *jobArchiveRunner) archivePreparedMedia(ctx context.Context, target *entity.ArchiveMediaTarget, session mediapkg.WriteSession) error {
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
	var skipped []skippedItem
	copyErr := a.transition(archiveStateCopying)
	if copyErr == nil {
		skipped, copyErr = a.copyArchiveItems(ctx, session)
		if transitionErr := a.transition(archiveStateFinalizingMedia); transitionErr != nil {
			copyErr = errors.Join(copyErr, transitionErr)
		}
	}

	// Let the Backend establish the physical result without inheriting the operation deadline.
	cleanupCtx := context.WithoutCancel(ctx)
	result, finalizeErr := session.Finalize(cleanupCtx, copyErr)
	if result != nil && result.Stop != nil && !errors.Is(copyErr, result.Stop) {
		copyErr = errors.Join(copyErr, result.Stop)
	}
	noSpaceErr := targetNoSpaceError(copyErr)
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
	var stored *library.Media
	var bytes, files int64
	var commitErr error
	formattedTar := target.GetTape().GetMode() == entity.ArchiveTapeWriteMode_ARCHIVE_TAPE_WRITE_MODE_FORMAT &&
		result.Media.Profile.GetTape().GetFormat() == library.TapeFormatTarPAXV1 && finalizeErr == nil
	if formattedTar {
		stored, bytes, files, commitErr = a.commitFormattedTarMedia(cleanupCtx, result.Media)
	} else {
		stored, bytes, files, commitErr = a.commitStagedMedia(cleanupCtx, result.Media)
	}
	// Settle the left-out items against the published Catalog, which now includes this attempt's copies.
	if commitErr == nil {
		mediaID := result.Media.ID
		if stored != nil {
			mediaID = stored.ID
		}
		commitErr = a.settleSkipped(cleanupCtx, mediaID, skipped)
	}
	if commitErr == nil && (files > 0 || formattedTar) {
		if files > 0 {
			progress.UpdateSessionCurrent(bytes, files)
			progress.CommitSession()
			committed = true
			a.logArchiveMediaNoSpaceCheckpoint(cleanupCtx, stored.ID, files, bytes, terminationErr)
		}
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
		if err := recordArchivedContent(tx); err != nil {
			return err
		}
		if err := tx.Model(&Item{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_STAGED).
			Updates(map[string]any{
				"status": entity.CopyStatus_COPY_STATUS_SUBMITTED, "media_id": stored.ID, "result": nil,
			}).Error; err != nil {
			return fmt.Errorf("submit Archive items failed, %w", err)
		}
		return completeSettledJob(tx)
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return stored, bytes, files, nil
}

// completeSettledJob completes the Job when no manifest item remains pending.
func completeSettledJob(tx *gorm.DB) error {
	var pending int64
	if err := tx.Model(&Item{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).Count(&pending).Error; err != nil {
		return fmt.Errorf("count pending Archive items failed, %w", err)
	}
	if pending != 0 {
		return nil
	}
	// Completion moves the manifest position with the state: both describe the same Job.
	if err := tx.Model(&executor.JobRecord{}).Where("id = ?", 1).
		Updates(map[string]any{"status": entity.JobStatus_JOB_STATUS_COMPLETED, "checkpoint": entity.JobStatus_JOB_STATUS_COMPLETED}).Error; err != nil {
		return fmt.Errorf("complete Archive Job failed, %w", err)
	}
	return nil
}

// settleSkipped satisfies the items an attempt left out when the published Catalog confirms that
// mediaID holds their content or that it reached the target Media count; any other item stays
// PENDING, for example when the copy it relied on was not published. A satisfied item leaves the
// manifest: its File's version already follows the existing copies through covered-original
// reconciliation, and no later Media copies it.
func (a *jobArchiveRunner) settleSkipped(ctx context.Context, mediaID int64, skipped []skippedItem) error {
	var config Config
	if err := a.db.WithContext(ctx).First(&config, 1).Error; err != nil {
		return fmt.Errorf("read Archive config failed, %w", err)
	}
	target := config.Spec.GetTargetMediaCount()
	if target == 0 {
		return nil
	}

	// Classify the left-out items in bounded Catalog reads.
	var onMedia, reached []int64
	for start := 0; start < len(skipped); start += batchSize {
		page := skipped[start:min(start+batchSize, len(skipped))]
		signatures := make([][]byte, 0, len(page))
		for _, item := range page {
			signatures = append(signatures, item.signature)
		}
		content, err := a.exe.Lib().ContentMedia(ctx, signatures)
		if err != nil {
			return err
		}
		for _, item := range page {
			media := content[string(item.signature)]
			switch {
			case slices.Contains(media, mediaID):
				onMedia = append(onMedia, item.id)
			case len(media) >= int(target):
				reached = append(reached, item.id)
			}
		}
	}
	a.logger.WithContext(ctx).Infof("archive skipped files settled, media_id=%d target_media_count=%d skipped_already_on_media=%d skipped_target_reached=%d",
		mediaID, target, len(onMedia), len(reached))
	if len(onMedia)+len(reached) == 0 {
		return nil
	}

	// Remove the satisfied rows, count them and complete the Job together.
	err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ids := append(slices.Clone(onMedia), reached...)
		for start := 0; start < len(ids); start += batchSize {
			if err := tx.Where("id IN ? AND status = ?", ids[start:min(start+batchSize, len(ids))], entity.CopyStatus_COPY_STATUS_PENDING).
				Delete(&Item{}).Error; err != nil {
				return fmt.Errorf("settle skipped Archive items failed, %w", err)
			}
		}
		if err := tx.Model(&Config{}).Where("id = ?", 1).Updates(map[string]any{
			"skipped_already_on_media": gorm.Expr("skipped_already_on_media + ?", len(onMedia)),
			"skipped_target_reached":   gorm.Expr("skipped_target_reached + ?", len(reached)),
		}).Error; err != nil {
			return fmt.Errorf("record Archive skipped files failed, %w", err)
		}
		return completeSettledJob(tx)
	})
	if err != nil {
		return err
	}

	// The removed rows leave the manifest totals.
	var totalFiles, totalBytes int64
	if err := a.db.WithContext(ctx).Model(&Item{}).Select("count(id)", "coalesce(sum(size), 0)").Row().Scan(&totalFiles, &totalBytes); err != nil {
		return fmt.Errorf("query archive total progress failed, %w", err)
	}
	a.getProgress().SetGlobalTotal(totalBytes, totalFiles)
	return nil
}

// recordArchivedContent keeps each submitted item's content facts as the copy read them, the same
// facts Library publication recorded, because the copy result itself is dropped at submission.
func recordArchivedContent(tx *gorm.DB) error {
	var cursor int64
	for {
		var items []*Item
		if err := tx.Where("status = ? AND id > ?", entity.CopyStatus_COPY_STATUS_STAGED, cursor).
			Order("id").Limit(batchSize).Find(&items).Error; err != nil {
			return fmt.Errorf("query staged Archive items failed, %w", err)
		}
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			cursor = item.ID
			expected, result := item.Data.GetExpected(), item.Result
			if expected == nil || result == nil {
				return fmt.Errorf("staged Archive item is incomplete, id=%d", item.ID)
			}
			if bytes.Equal(expected.Sha256, result.Sha256) && expected.SizeBytes == result.SizeBytes && len(expected.Signature) > 0 {
				continue
			}
			signature := expected.Signature
			if !library.KeepsSignature(expected, result.SizeBytes, result.Sha256) {
				var err error
				if signature, err = library.NewFileSignature(result.Sha256, result.SizeBytes); err != nil {
					return err
				}
			}
			data := proto.Clone(item.Data).(*entity.ArchiveManifestFile)
			data.Expected.Signature, data.Expected.Sha256 = signature, result.Sha256
			data.Expected.SizeBytes, data.Expected.Mode, data.Expected.MtimeNs = result.SizeBytes, result.Mode, result.ModTimeNs
			if err := tx.Model(&Item{}).Where("id = ?", item.ID).Update("data", data).Error; err != nil {
				return fmt.Errorf("record archived content failed, id=%d, %w", item.ID, err)
			}
		}
	}
}

// copyArchiveItems copies the pending manifest to the session and returns the items it left out
// because their known content needed no copy on this Media.
func (a *jobArchiveRunner) copyArchiveItems(ctx context.Context, session mediapkg.WriteSession) ([]skippedItem, error) {
	// One attempt reads its pipeline tunables from the Library settings that own them.
	settings, err := executor.JobExecutionSettings(ctx)
	if err != nil {
		return nil, err
	}

	// The results callback only hands outcomes to the writer; the shared writer stages each batch
	// of them outside the feed, so the copy pipeline never waits for a database write.
	reporter, err := newReporter(context.WithoutCancel(ctx), a)
	if err != nil {
		return nil, err
	}
	// The writer is drained on every return path; Close is idempotent, so the run path below reads
	// its terminal error again.
	defer func() { _ = reporter.Close() }()
	// The Job's frozen choice decides whether reading an original also writes its signature cache;
	// an unset choice keeps writing it.
	var config Config
	if err := a.db.WithContext(ctx).First(&config, 1).Error; err != nil {
		return nil, fmt.Errorf("read Archive config failed, %w", err)
	}
	hashPolicy := acp.HashRead
	if choice := config.Spec.WriteSignatureCache; choice == nil || *choice {
		hashPolicy = acp.HashReadRefresh
	}

	source := &copySource{runner: a, session: session, pageSize: int(settings.GetReadBatch()),
		target: config.Spec.GetTargetMediaCount(), written: make(map[string]bool)}
	if media := session.Media(); media != nil {
		source.mediaID = media.ID
	}
	options := []acp.Option{
		acp.WithHashPolicy(hashPolicy),
		acp.SetToDevice(mediapkg.DeviceOptions(session.Capabilities().Write)...),
		acp.WithLogger(a.logger),
		acp.WithEventHandler(a.archiveEventHandler(ctx)),
	}
	engine, err := acp.NewStream(ctx, reporter.onResults, append(options, executor.ACPOptions(settings)...)...)
	if err != nil {
		return nil, err
	}
	copyErr := source.consume(ctx, engine)

	// A result no durable row can express does not stop the feed, so the attempt reports it here;
	// a failed staging write is the writer's terminal error and is reported the same way.
	for _, err := range []error{reporter.Close(), reporter.err()} {
		if err == nil || errors.Is(copyErr, err) {
			continue
		}
		if copyErr == nil {
			copyErr = err
		} else {
			copyErr = multierror.Append(copyErr, err).ErrorOrNil()
		}
	}
	if copyErr == nil {
		return source.skipped, nil
	}
	return source.skipped, fmt.Errorf("stream Archive copy failed, %w", copyErr)
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
	if err := a.db.WithContext(ctx).Model(&Item{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_STAGED).
		Updates(map[string]any{
			"status": entity.CopyStatus_COPY_STATUS_PENDING, "media_path": "", "media_id": nil, "result": nil,
		}).Error; err != nil {
		return fmt.Errorf("reset staged Archive items failed, %w", err)
	}
	return nil
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

// commitFormattedTarMedia also publishes a successfully initialized cartridge with no complete members.
func (a *jobArchiveRunner) commitFormattedTarMedia(ctx context.Context, descriptor *mediapkg.Descriptor) (*library.Media, int64, int64, error) {
	// Ordinary completed-member publication keeps its existing Job checkpoint behavior.
	stored, bytes, files, err := a.commitStagedMedia(ctx, descriptor)
	if err != nil || files != 0 {
		return stored, bytes, files, err
	}

	// A usable FORMAT result proves the empty identity; no failed finalization reaches this branch.
	stored, err = a.exe.Lib().CommitMedia(ctx, stored, func(context.Context, func(*library.MediaFile) error) error { return nil })
	if err != nil {
		return nil, 0, 0, fmt.Errorf("commit empty tar Media failed, %w", err)
	}
	if err := a.exe.AddJobProperties(ctx, a.job.ID, executor.JobProperty{Key: executor.JobPropertyMedia, Value: stored.ID}); err != nil {
		return nil, 0, 0, err
	}
	return stored, 0, 0, nil
}
