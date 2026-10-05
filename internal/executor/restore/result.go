package restore

import (
	"context"
	"fmt"
	"os"
	"path"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *jobRestoreRunner) completeOutput(ctx context.Context, copy *copyCandidate, hash []byte, size int64, damaged bool) error {
	// The same verified final file supplies restore metadata and the new original observation.
	full, err := a.exe.RestoreOutputPath(ctx, a.destination, copy.TargetPath)
	if err != nil {
		return err
	}
	if a.destination.GetLocationId() != 0 {
		owner, err := a.exe.Lib().GetFileLocationAtPath(ctx, a.destination.LocationId, path.Join(a.destination.Path, copy.TargetPath))
		if err != nil {
			return err
		}
		if owner != nil {
			return fmt.Errorf("Restore output is already linked to File %d", owner.FileID)
		}
	}
	if !damaged && copy.FileVersionID != 0 {
		if err := a.restoreMetadata(copy.TargetPath, copy.Mode, copy.MtimeNS); err != nil {
			return err
		}
	}
	info, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Restore output is not an ordinary file: %q", copy.TargetPath)
	}
	facts, err := executor.InspectLocationFacts(info)
	if err != nil {
		return err
	}
	result := &library.RestoreResult{SourceFileID: copy.FileID, SourceVersionID: copy.FileVersionID, LocationID: a.destination.GetLocationId(),
		Path:      path.Join(a.destination.GetPath(), copy.TargetPath),
		Signature: copy.Signature, ExpectedHash: copy.Hash, ExpectedSize: copy.Size, ActualHash: hash, ActualSize: size}
	if damaged {
		result.Outcome = library.RestoreDamaged
	}
	if a.destination.GetLocationId() == 0 {
		return a.checkpointResult(ctx, copy, result)
	}

	// Ignore suppresses indexing, not recovery; ignored and damaged bytes remain restore-only outputs.
	location, err := a.exe.Lib().GetLocation(ctx, a.destination.LocationId)
	if err != nil {
		return err
	}
	var selected File
	if err := a.db.WithContext(ctx).First(&selected, copy.ItemID).Error; err != nil {
		return err
	}
	publication := &library.RestorePublication{Result: *result, Reconnect: selected.Reconnect,
		ParentID: selected.ParentID, Name: selected.Name,
		Version: &library.FileVersion{ID: copy.FileVersionID, FileID: copy.FileID, Signature: copy.Signature,
			Hash: copy.Hash, Size: copy.Size, Mode: copy.Mode, MtimeNS: copy.MtimeNS, LastArchivedAtNS: selected.LastArchivedAtNS},
		Original: &library.FileLocation{Size: size, Mode: facts.Mode, MtimeNS: facts.MtimeNs, Hash: hash}}
	if !damaged && !location.Ignored(result.Path, false) {
		publication.Tracking = executor.ObserveTracking(location, info)
	}
	result, err = a.exe.Lib().PublishRestore(ctx, publication)
	if err != nil {
		return err
	}
	return a.checkpointResult(ctx, copy, result)
}

func (a *jobRestoreRunner) checkpointResult(ctx context.Context, copy *copyCandidate, result *library.RestoreResult) error {
	// Keep per-item output facts and all equivalent Media candidates in one Job checkpoint.
	return a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		message := "Restored"
		switch result.Outcome {
		case library.RestoreReconnected:
			message = "Original reconnected"
		case library.RestoreNewFile:
			message = "Restored as a new File"
		case library.RestoreIgnored:
			message = "Restored without linking: ignored path"
		case library.RestoreDamaged:
			message = "Damaged output retained; not linked"
		}
		output := &File{ItemID: copy.ItemID, Path: copy.TargetPath, Completed: true, ResultFileID: result.ResultFileID,
			Linked: result.ResultFileID != 0, Damaged: result.Outcome == library.RestoreDamaged,
			ActualHash: result.ActualHash, ActualSize: result.ActualSize, ResultMessage: message}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "item_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"completed", "result_file_id", "linked", "damaged", "actual_hash", "actual_size", "result_message"}),
		}).Create(output).Error; err != nil {
			return err
		}
		updated := tx.Model(&Copy{}).Where("item_id = ?", copy.ItemID).Update("status", entity.CopyStatus_COPY_STATUS_COMPLETED)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			return fmt.Errorf("Restore item is missing, item_id=%d", copy.ItemID)
		}

		// Indexing owns its final checkpoint; execution completes when every distinct item is satisfied.
		var pending int64
		if err := tx.Model(&Copy{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).Limit(1).Count(&pending).Error; err != nil {
			return err
		}
		if pending != 0 {
			return nil
		}
		// Completion moves the manifest position with the state: both describe the same Job.
		return tx.Model(&executor.JobRecord{}).Where("id = ? AND status = ?", 1, entity.JobStatus_JOB_STATUS_READY).
			Updates(map[string]any{"status": entity.JobStatus_JOB_STATUS_COMPLETED, "checkpoint": entity.JobStatus_JOB_STATUS_COMPLETED}).Error
	})
}
