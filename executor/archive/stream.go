package archive

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	mediapkg "github.com/samuelncui/yatm/media"
)

type copySource struct {
	runner    *jobArchiveRunner
	session   mediapkg.WriteSession
	cursor    string
	page      []*Item
	index     int
	started   bool
	locations map[int64]bool
}

func (s *copySource) Next(ctx context.Context) (*acp.StreamRequest, error) {
	// Load the next short page after the previous page is fully consumed.
	if s.index == len(s.page) {
		var page []*Item
		if err := s.runner.db.WithContext(ctx).
			Where("status = ? AND target_path > ?", entity.CopyStatus_PENDING, s.cursor).
			Order("target_path").Limit(batchSize).Find(&page).Error; err != nil {
			return nil, fmt.Errorf("query archive copy page failed, cursor=%q, %w", s.cursor, err)
		}
		if len(page) == 0 {
			return nil, io.EOF
		}
		s.page = page
		s.index = 0
		s.locations = make(map[int64]bool)
	}

	// Translate one manifest row without retaining it after the bounded page advances.
	item := s.page[s.index]
	s.index++
	s.cursor = item.TargetPath
	if item.Data.Expected == nil {
		if err := s.runner.captureRawInput(ctx, item); err != nil {
			return nil, err
		}
		if err := s.runner.db.WithContext(ctx).Model(item).Update("data", item.Data).Error; err != nil {
			return nil, err
		}
	}
	if item.Data.LibrarySelected {
		filename, locationID, err := s.runner.exe.ResolveOnlineFile(ctx, item.Data.Expected)
		if err != nil {
			return nil, err
		}
		// Capture a relocated original when used; the initial frozen source remains historical.
		if locationID != item.Data.Expected.OriginalLocationId && !s.locations[locationID] {
			if err := s.runner.exe.AddJobResources(ctx, s.runner.job.ID, executor.JobResource{
				Kind: executor.JobResourceLocation, Role: executor.JobResourceSource, ResourceID: locationID,
			}); err != nil {
				return nil, err
			}
			s.locations[locationID] = true
		}
		item.Data.SourcePath = filename
		if err := s.runner.db.WithContext(ctx).Model(&Item{}).Where("id = ? AND status = ?", item.ID, entity.CopyStatus_PENDING).Update("data", item.Data).Error; err != nil {
			return nil, err
		}
	}
	target, err := s.session.TargetPath(item.TargetPath)
	if errors.Is(err, mediapkg.ErrCapacityBoundary) {
		if !s.started {
			return nil, fmt.Errorf(
				"archive file does not fit on Media, path=%q size=%d, %w",
				item.TargetPath, item.Size, mediapkg.ErrCapacityBoundary,
			)
		}
		return nil, fmt.Errorf(
			"archive Media reached capacity before file, path=%q size=%d, %w",
			item.TargetPath, item.Size, mediapkg.ErrCapacityBoundary,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("resolve archive Media target failed, path=%q, %w", item.TargetPath, err)
	}
	s.started = true
	return &acp.StreamRequest{
		ID:      item.ID,
		Source:  item.Data.SourcePath,
		Targets: []string{target},
	}, nil
}

type copySink struct {
	runner *jobArchiveRunner
}

func (s *copySink) Write(ctx context.Context, result *acp.StreamResult) error {
	return s.runner.stageCopyResult(ctx, result)
}

func (s *copySink) Flush(context.Context) error {
	return nil
}

func (a *jobArchiveRunner) stageCopyResult(
	ctx context.Context,
	result *acp.StreamResult,
) error {
	// Resolve exactly one ACP completion to its immutable manifest row.
	if result == nil || result.Job == nil {
		return fmt.Errorf("archive copy result is nil")
	}
	item := new(Item)
	if err := a.db.WithContext(ctx).Where("id = ? AND status = ?", result.ID, entity.CopyStatus_PENDING).
		First(item).Error; err != nil {
		return fmt.Errorf("query archive result item failed, id=%d, %w", result.ID, err)
	}

	// Validate ACP's streamed result without rereading the source or target.
	job := result.Job
	if job.Status != acp.JobStatusFinished {
		return fmt.Errorf("archive file did not finish, source=%q", item.Data.SourcePath)
	}
	if filepath.Clean(job.FullPath) != item.Data.SourcePath {
		return fmt.Errorf("archive copy result path mismatch, source=%q", item.Data.SourcePath)
	}
	if len(job.SuccessTargets) == 0 {
		if len(job.FailTargets) == 0 {
			return fmt.Errorf("archive file has no target result, source=%q", item.Data.SourcePath)
		}
		return nil
	}
	if len(job.SuccessTargets) != 1 || len(job.FailTargets) != 0 {
		return fmt.Errorf("archive file has inconsistent target result, source=%q", item.Data.SourcePath)
	}
	if !job.Mode.IsRegular() || job.Size != item.Size {
		return fmt.Errorf("archive file changed during copy, source=%q", item.Data.SourcePath)
	}
	hash, err := hex.DecodeString(job.SHA256)
	if err != nil || len(hash) != 32 {
		return fmt.Errorf("archive file has invalid SHA-256, source=%q", item.Data.SourcePath)
	}
	if expected := item.Data.Expected; expected != nil {
		if expected.Size != job.Size || !bytes.Equal(expected.Sha256, hash) {
			return fmt.Errorf("Archive source no longer contains selected File %d; synchronize and retry with matching content", expected.FileId)
		}
	}
	copyResult := &entity.ArchiveCopyResult{
		Size:        job.Size,
		Mode:        uint32(job.Mode),
		ModTimeNs:   job.ModTime.UnixNano(),
		WriteTimeNs: job.WriteTime.UnixNano(),
		Sha256:      hash,
	}
	if err := copyResult.Validate(); err != nil {
		return err
	}
	mediaPath := item.TargetPath
	if err := entity.ValidateRelativePath(mediaPath); err != nil {
		return fmt.Errorf("invalid archive Media path, source=%q, %w", item.Data.SourcePath, err)
	}

	// Persist the batch-relative Media path only after ACP reports success.
	updated := a.db.WithContext(ctx).Model(&Item{}).Where("id = ? AND status = ?", item.ID, entity.CopyStatus_PENDING).
		Updates(map[string]any{
			"status": entity.CopyStatus_STAGED, "media_path": mediaPath, "result": copyResult,
		})
	if updated.Error != nil {
		return fmt.Errorf("stage archive item failed, id=%d, %w", item.ID, updated.Error)
	}
	if updated.RowsAffected != 1 {
		return fmt.Errorf("stage archive item failed, id=%d affected=%d", item.ID, updated.RowsAffected)
	}
	return nil
}
