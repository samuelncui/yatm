package restore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/media"
)

func (a *jobRestoreRunner) loadCopyPage(
	ctx context.Context,
	mediaID int64,
	sequential bool,
	cursorOrder []byte,
	cursorPath string,
	cursorID int64,
	limit int,
) ([]*copyCandidate, error) {
	// Select the next bounded page in the access order required by this Media.
	query := a.db.WithContext(ctx).
		Where("status = ? AND media_id = ?", entity.CopyStatus_COPY_STATUS_PENDING, mediaID)
	if cursorID != 0 {
		if sequential {
			query = query.Where(
				"storage_order > ? OR "+
					"(storage_order = ? AND media_path > ?) OR "+
					"(storage_order = ? AND media_path = ? AND id > ?)",
				cursorOrder, cursorOrder, cursorPath, cursorOrder, cursorPath, cursorID,
			)
		} else {
			query = query.Where("media_path > ? OR (media_path = ? AND id > ?)", cursorPath, cursorPath, cursorID)
		}
	}
	var copies []*Copy
	order := "media_path, id"
	if sequential {
		order = "storage_order, media_path, id"
	}
	if err := query.Order(order).Limit(limit).Find(&copies).Error; err != nil {
		return nil, fmt.Errorf(
			"query Restore copy page failed, media_id=%d storage_order=%x media_path=%q id=%d, %w",
			mediaID,
			cursorOrder,
			cursorPath,
			cursorID,
			err,
		)
	}
	candidates, err := a.copyCandidates(ctx, copies)
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

// copyJob is one caller-owned Restore item: the frozen candidate it transfers and the paths ACP
// must use. It is pure data, so the results callback recovers the candidate by asserting
// Result.Job back to this type.
type copyJob struct {
	source string
	target string
	copy   *copyCandidate
}

func (j *copyJob) Source() string { return j.source }

func (j *copyJob) Targets() []string { return []string{j.target} }

// copyItems pages the Job manifest into caller-owned items. It never blocks on physical I/O;
// every result is persisted by the results callback that receives it.
type copyItems struct {
	runner  *jobRestoreRunner
	mediaID int64
	session media.ReadSession
	batch   int

	cursorOrder []byte
	cursorPath  string
	cursorID    int64
	failure     error
}

// nextPage returns the next bounded page of transferable candidates, or io.EOF once the
// manifest is exhausted.
func (s *copyItems) nextPage(ctx context.Context) ([]acp.Item, error) {
	items := make([]acp.Item, 0, s.batch)
	for len(items) < s.batch {
		page, err := s.runner.loadCopyPage(
			ctx, s.mediaID, s.session.Capabilities().Read == media.AccessSequential,
			s.cursorOrder, s.cursorPath, s.cursorID, s.batch,
		)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		for _, copy := range page {
			s.advance(copy)
			item, err := s.resolve(ctx, copy)
			if err != nil {
				// Local failures leave this candidate pending while independent files continue.
				var local *restoreItemError
				if !errors.As(err, &local) {
					return nil, err
				}
				if recordErr := s.runner.recordItemFailure(ctx, copy, err); recordErr != nil {
					return nil, errors.Join(err, recordErr)
				}
				if s.failure == nil {
					s.failure = err
				}
				continue
			}
			if item == nil {
				continue
			}
			items = append(items, item)
			if len(items) == s.batch {
				break
			}
		}
	}
	if len(items) == 0 {
		return nil, io.EOF
	}
	return items, nil
}

func (s *copyItems) consume(ctx context.Context, engine *acp.StreamCopyer) error {
	return errors.Join(executor.RunCopyPages(ctx, engine, s.nextPage), s.failure)
}

func (s *copyItems) advance(copy *copyCandidate) {
	s.cursorOrder = copy.StorageOrder
	s.cursorPath = copy.MediaPath
	s.cursorID = copy.ID
}

// resolve turns one pending candidate into an item ACP must transfer. A candidate whose
// output already holds verified bytes needs no transfer: its staged facts are recorded
// while the batch is resolved, and no item is returned.
func (s *copyItems) resolve(ctx context.Context, copy *copyCandidate) (acp.Item, error) {
	var output File
	if err := s.runner.db.WithContext(ctx).Where("item_id = ?", copy.ItemID).Limit(1).Find(&output).Error; err != nil {
		return nil, err
	}
	if output.Ready {
		if output.ReadMediaID != s.mediaID {
			return nil, fmt.Errorf("pending Restore output requires finalization of Media %d", output.ReadMediaID)
		}
		matched, _, err := s.runner.outputMatches(ctx, output.Path, output.ActualHash, output.ActualSize)
		if err != nil {
			return nil, err
		}
		if !matched {
			return nil, &restoreItemError{fmt.Errorf("pending Restore output changed: %q", output.Path)}
		}
		return nil, s.runner.stageOutput(ctx, copy, output.ActualHash, output.ActualSize, output.Damaged)
	}

	matched, exists, err := s.runner.outputMatches(ctx, copy.TargetPath, copy.Hash, copy.Size)
	if err != nil {
		return nil, err
	}
	if matched {
		if output.ReadMediaID != 0 && output.ReadMediaID != s.mediaID {
			return nil, fmt.Errorf("pending Restore output requires finalization of Media %d", output.ReadMediaID)
		}
		return nil, s.runner.stageOutput(ctx, copy, copy.Hash, copy.Size, false)
	}
	if exists {
		return nil, &restoreItemError{fmt.Errorf("reserved Restore output changed: %q", copy.TargetPath)}
	}
	source, err := s.session.SourcePath(copy.MediaPath)
	if err != nil {
		// A candidate this attempt cannot reach is observed and reported exactly as a
		// failed read is.
		if recordErr := s.runner.observeReadFailure(ctx, copy, err); recordErr != nil {
			return nil, errors.Join(err, recordErr)
		}
		return nil, &restoreItemError{fmt.Errorf("resolve Restore Media source failed, path=%q, %w", copy.MediaPath, err)}
	}
	if err := s.runner.beginOutput(ctx, copy); err != nil {
		return nil, err
	}
	return &copyJob{source: source, target: s.runner.restoreTarget(copy.TargetPath), copy: copy}, nil
}

func (a *jobRestoreRunner) observeReadFailure(ctx context.Context, copy *copyCandidate, readErr error) error {
	// Only item-local, definite failures become health facts; interruption and device loss remain attempt errors.
	health := entity.PositionHealth_POSITION_HEALTH_UNKNOWN
	switch {
	case os.IsNotExist(readErr):
		health = entity.PositionHealth_POSITION_HEALTH_MISSING
	case os.IsPermission(readErr):
		health = entity.PositionHealth_POSITION_HEALTH_UNREADABLE
	default:
		return nil
	}
	return a.db.WithContext(ctx).Model(&Copy{}).Where("id = ?", copy.ID).
		Updates(map[string]any{"health": health, "health_checked_at_ns": time.Now().UnixNano(), "health_published": false}).Error
}

// storeCompletion persists one finished item through the results callback, so a Media attempt
// has exactly one place that records transfer outcomes.
func (a *jobRestoreRunner) storeCompletion(
	ctx context.Context,
	mediaID int64,
	session media.ReadSession,
	job *copyJob,
	result acp.Result,
) error {
	if result.Err != nil {
		// ACP could not process the item or abandoned it during a graceful stop: the copy
		// keeps its PENDING status and the attempt reports why.
		if err := a.observeReadFailure(ctx, job.copy, result.Err); err != nil {
			return err
		}
		return itemFailure(job, result.Err)
	}
	damaged, err := a.completeCopy(ctx, session, job, result)
	if err != nil {
		return err
	}
	// The staged facts come from ACP's real transferred bytes, never from a reread target.
	if err := a.stageOutput(ctx, job.copy, result.SHA256, result.Size, damaged); err != nil {
		return err
	}
	// The per-file completion line stays where the item's outcome is known, so a stopped attempt
	// logs exactly the files whose results it recorded.
	a.logger.WithContext(ctx).Infof("restore file finished, source=%q size=%d", job.source, result.Size)
	return nil
}

// itemFailure describes an item ACP did not transfer, preserving the original error.
type restoreItemError struct{ error }

func (e *restoreItemError) Unwrap() error { return e.error }

func itemFailure(job *copyJob, err error) error {
	if err == nil {
		err = fmt.Errorf("ACP abandoned the Restore item, media_path=%q", job.copy.MediaPath)
	}
	return &restoreItemError{err}
}

func (a *jobRestoreRunner) recordItemFailure(ctx context.Context, copy *copyCandidate, err error) error {
	// Preserve a per-file explanation without resetting ready outputs or other candidates.
	if writeErr := a.db.WithContext(ctx).Model(&File{}).Where("item_id = ? AND completed = ?", copy.ItemID, false).
		Update("result_message", err.Error()).Error; writeErr != nil {
		return writeErr
	}
	a.logger.WithContext(ctx).WithError(err).Warnf("Restore file failed, media_path=%q", copy.MediaPath)
	return nil
}

// completeCopy validates one ACP completion against the frozen candidate. It reports
// whether the transferred bytes are damaged and are still eligible for Restore, and returns
// an error only when the completion carries no verified output for this candidate.
func (a *jobRestoreRunner) completeCopy(
	ctx context.Context,
	session media.ReadSession,
	job *copyJob,
	result acp.Result,
) (bool, error) {
	copy := job.copy

	// Validate ACP's real transferred facts without rereading the restored file.
	source, err := session.SourcePath(copy.MediaPath)
	if err != nil {
		return false, itemFailure(job, fmt.Errorf("resolve Restore result source failed, path=%q, %w", copy.MediaPath, err))
	}
	if job.source != source {
		return false, itemFailure(job, fmt.Errorf("restore copy result source mismatch, media_path=%q", copy.MediaPath))
	}
	if len(result.Targets) != 1 {
		return false, itemFailure(job, fmt.Errorf("restore file did not finish, media_path=%q", copy.MediaPath))
	}
	target := &result.Targets[0]
	if target.Err != nil {
		// Target errors keep their identity, so an exhausted destination stays recognizable.
		return false, itemFailure(job, fmt.Errorf("restore target failed, media_path=%q, %w", copy.MediaPath, target.Err))
	}
	if filepath.Clean(target.Path) != a.restoreTarget(copy.TargetPath) {
		return false, itemFailure(job, fmt.Errorf("restore copy result path mismatch, media_path=%q", copy.MediaPath))
	}
	if len(result.SHA256) != 32 {
		return false, itemFailure(job, fmt.Errorf("Restore returned invalid content hash, media_path=%q", copy.MediaPath))
	}

	// Record the read outcome before deciding whether its bytes may be published.
	damaged := result.Size != copy.Size || !bytes.Equal(result.SHA256, copy.Hash)
	health := entity.PositionHealth_POSITION_HEALTH_HEALTHY
	if damaged {
		health = entity.PositionHealth_POSITION_HEALTH_DAMAGED
	}
	if err := a.db.WithContext(ctx).Model(&Copy{}).Where("id = ?", copy.ID).
		Updates(map[string]any{"health": health, "health_checked_at_ns": time.Now().UnixNano(), "health_published": false}).Error; err != nil {
		return false, err
	}
	if !damaged || a.allowDamaged {
		return damaged, nil
	}

	// Only a complete read can produce salvage. ACP owns interrupted-target cleanup, and a
	// rejected mismatch is removed so the same operation can retry it.
	mismatch := fmt.Errorf("restore file checksum mismatch, media_path=%q", copy.MediaPath)
	if removeErr := os.Remove(a.restoreTarget(copy.TargetPath)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return false, itemFailure(job, errors.Join(mismatch, removeErr))
	}
	return false, itemFailure(job, mismatch)
}
