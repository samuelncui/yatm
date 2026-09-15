package restore

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
)

func (a *jobRestoreRunner) loadCopyPage(
	ctx context.Context,
	mediaID int64,
	sequential bool,
	cursorOrder []byte,
	cursorPath string,
	cursorID int64,
) ([]*Copy, error) {
	// Select the next bounded page in the access order required by this Media.
	query := a.db.WithContext(ctx).
		Where("status = ? AND media_id = ?", entity.CopyStatus_PENDING, mediaID)
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
	if err := query.Order(order).Limit(batchSize).Find(&copies).Error; err != nil {
		return nil, fmt.Errorf(
			"query Restore copy page failed, media_id=%d storage_order=%x media_path=%q id=%d, %w",
			mediaID,
			cursorOrder,
			cursorPath,
			cursorID,
			err,
		)
	}
	return copies, nil
}

type copySource struct {
	runner      *jobRestoreRunner
	mediaID     int64
	session     mediapkg.ReadSession
	cursorOrder []byte
	cursorPath  string
	cursorID    int64
	page        []*Copy
	index       int
}

func (s *copySource) Next(ctx context.Context) (*acp.StreamRequest, error) {
	for {
		// Pull the next pending page without interrupting the ACP stream.
		if s.index == len(s.page) {
			page, err := s.runner.loadCopyPage(
				ctx, s.mediaID, s.session.Capabilities().Read == mediapkg.AccessSequential,
				s.cursorOrder, s.cursorPath, s.cursorID,
			)
			if err != nil {
				return nil, err
			}
			if len(page) == 0 {
				return nil, io.EOF
			}
			s.page = page
			s.index = 0
		}

		// Copy one physical candidate directly to its final target.
		copy := s.page[s.index]
		s.index++
		s.cursorOrder = copy.StorageOrder
		s.cursorPath = copy.MediaPath
		s.cursorID = copy.ID
		var output Output
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
				return nil, fmt.Errorf("pending Restore output changed: %q", output.Path)
			}
			continue
		}
		matched, exists, err := s.runner.outputMatches(ctx, copy.TargetPath, copy.Hash, copy.Size)
		if err != nil {
			return nil, err
		}
		if matched {
			if output.ReadMediaID != 0 && output.ReadMediaID != s.mediaID {
				return nil, fmt.Errorf("pending Restore output requires finalization of Media %d", output.ReadMediaID)
			}
			if err := s.runner.stageOutput(ctx, copy, copy.Hash, copy.Size, false); err != nil {
				return nil, err
			}
			continue
		}
		if exists {
			return nil, fmt.Errorf("reserved Restore output changed: %q", copy.TargetPath)
		}
		if err := s.runner.checkCandidate(ctx, copy); err != nil {
			return nil, err
		}
		source, err := s.session.SourcePath(copy.MediaPath)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("resolve Restore Media source failed, path=%q, %w", copy.MediaPath, err), s.runner.observeReadFailure(ctx, copy, err))
		}
		if err := s.runner.beginOutput(ctx, copy); err != nil {
			return nil, err
		}
		return &acp.StreamRequest{
			ID:      copy.ID,
			Source:  source,
			Targets: []string{s.runner.restoreTarget(copy.TargetPath)},
		}, nil
	}
}

func (a *jobRestoreRunner) observeReadFailure(ctx context.Context, copy *Copy, readErr error) error {
	// Only item-local, definite failures become health facts; interruption and device loss remain attempt errors.
	health := entity.PositionHealth_POSITION_HEALTH_UNKNOWN
	switch {
	case os.IsNotExist(readErr):
		health = entity.PositionHealth_MISSING
	case os.IsPermission(readErr):
		health = entity.PositionHealth_UNREADABLE
	default:
		return nil
	}
	return a.db.WithContext(ctx).Model(&Copy{}).Where("id = ?", copy.ID).
		Updates(map[string]any{"health": health, "health_checked_at": time.Now().UnixMilli(), "health_published": false}).Error
}

type copySink struct {
	runner  *jobRestoreRunner
	mediaID int64
	session mediapkg.ReadSession
}

func (s *copySink) Write(ctx context.Context, result *acp.StreamResult) error {
	return s.runner.completeCopy(ctx, s.mediaID, s.session, result)
}

func (s *copySink) Flush(context.Context) error {
	return nil
}

func (a *jobRestoreRunner) completeCopy(
	ctx context.Context,
	mediaID int64,
	session mediapkg.ReadSession,
	result *acp.StreamResult,
) error {
	// Resolve exactly one ACP completion to its immutable candidate row.
	if result == nil || result.Job == nil {
		return fmt.Errorf("restore copy result is nil")
	}
	copy := new(Copy)
	if err := a.db.WithContext(ctx).
		Where("id = ? AND media_id = ? AND status = ?", result.ID, mediaID, entity.CopyStatus_PENDING).
		First(copy).Error; err != nil {
		return fmt.Errorf("query restore result copy failed, id=%d, %w", result.ID, err)
	}

	// Validate ACP's streamed checksum without rereading the restored file.
	job := result.Job
	expectedSource, err := session.SourcePath(copy.MediaPath)
	if err != nil {
		return fmt.Errorf("resolve Restore result source failed, path=%q, %w", copy.MediaPath, err)
	}
	expectedTarget := a.restoreTarget(copy.TargetPath)
	if job.Status != acp.JobStatusFinished || len(job.SuccessTargets) != 1 || len(job.FailTargets) != 0 {
		return fmt.Errorf("restore file did not finish, media_path=%q", copy.MediaPath)
	}
	if filepath.Clean(job.FullPath) != expectedSource || filepath.Clean(job.SuccessTargets[0]) != expectedTarget {
		return fmt.Errorf("restore copy result path mismatch, media_path=%q", copy.MediaPath)
	}
	hash, err := hex.DecodeString(job.SHA256)
	if err != nil || len(hash) != 32 {
		return fmt.Errorf("Restore returned invalid content hash, media_path=%q", copy.MediaPath)
	}
	damaged := job.Size != copy.Size || !bytes.Equal(hash, copy.Hash)
	health := entity.PositionHealth_HEALTHY
	if damaged {
		health = entity.PositionHealth_DAMAGED
	}
	if err := a.db.WithContext(ctx).Model(&Copy{}).Where("id = ?", copy.ID).
		Updates(map[string]any{"health": health, "health_checked_at": time.Now().UnixMilli(), "health_published": false}).Error; err != nil {
		return err
	}

	// Only a complete read can produce salvage. ACP owns interrupted-target cleanup.
	if damaged && !a.allowDamaged {
		mismatch := fmt.Errorf("restore file checksum mismatch, media_path=%q", copy.MediaPath)
		return errors.Join(mismatch, os.Remove(expectedTarget))
	}
	return a.stageOutput(ctx, copy, hash, job.Size, damaged)
}

func (a *jobRestoreRunner) checkCandidate(ctx context.Context, copy *Copy) error {
	// Frozen legacy candidates predate Position provenance; their actual transferred bytes remain verified.
	if copy.PositionID == 0 {
		return nil
	}
	position, err := a.exe.Lib().GetPosition(ctx, copy.PositionID)
	if err != nil {
		return err
	}
	token, err := library.PositionContentToken(position)
	if err != nil {
		return err
	}
	if !bytes.Equal(token, copy.PositionContentToken) {
		return fmt.Errorf("Restore archive Position changed, id=%d", copy.PositionID)
	}
	usable := library.PositionRestoreEligible(position.Health) || a.allowDamaged &&
		(position.Health == entity.PositionHealth_DAMAGED || position.Health == entity.PositionHealth_UNREADABLE)
	if !usable {
		return fmt.Errorf("Restore copy is known unusable, id=%d health=%s", position.ID, position.Health)
	}
	return nil
}
