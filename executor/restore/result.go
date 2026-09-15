package restore

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (a *jobRestoreRunner) destinationAdmission() (func(), error) {
	if a.destination.GetLocationId() == 0 {
		return a.exe.Lib().UseOnlineRead()
	}
	return a.exe.Lib().UseOnlineSource(a.destination.LocationId)
}

func (a *jobRestoreRunner) restoreResult(ctx context.Context, copy *Copy) (*library.RestoreResult, error) {
	// legacy frozen Jobs never guess an original association from legacy numeric identities.
	if a.destination.GetLocationId() == 0 {
		return nil, nil
	}
	result, err := a.exe.Lib().GetRestoreResult(ctx, a.operationID, copy.ItemID)
	if err != nil || result == nil {
		return result, err
	}
	if result.SourceFileID != copy.FileID || result.SourceVersionID != copy.FileVersionID ||
		result.LocationID != a.destination.LocationId || result.BindingToken != a.destination.BindingToken ||
		result.Path != path.Join(a.destination.Path, copy.TargetPath) || !bytes.Equal(result.Signature, copy.Signature) ||
		!bytes.Equal(result.ExpectedHash, copy.Hash) || result.ExpectedSize != copy.Size {
		return nil, fmt.Errorf("Restore result does not belong to the frozen item")
	}
	return result, nil
}

func (a *jobRestoreRunner) completeOutput(ctx context.Context, copy *Copy, hash []byte, size int64, damaged bool) error {
	// A committed Library result wins even when a subsequent Job checkpoint was interrupted.
	result, err := a.restoreResult(ctx, copy)
	if err != nil {
		return err
	}
	if result != nil {
		return a.checkpointResult(ctx, copy, result)
	}

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
	if !info.Mode().IsRegular() || info.Size() != size {
		return fmt.Errorf("Restore output changed before publication: %q", copy.TargetPath)
	}
	result = &library.RestoreResult{OperationID: a.operationID, ItemID: copy.ItemID,
		SourceFileID: copy.FileID, SourceVersionID: copy.FileVersionID, LocationID: a.destination.GetLocationId(),
		BindingToken: a.destination.GetBindingToken(), Path: path.Join(a.destination.GetPath(), copy.TargetPath),
		Signature: copy.Signature, ExpectedHash: copy.Hash, ExpectedSize: copy.Size, ActualHash: hash, ActualSize: size}
	if damaged {
		result.Outcome = library.RestoreDamaged
	}
	if a.destination.GetLocationId() == 0 {
		return a.checkpointResult(ctx, copy, result)
	}

	// Ignore suppresses indexing, not recovery; ignored and damaged bytes remain restore-only outputs.
	location, err := a.exe.Lib().GetOnlineSource(ctx, a.destination.LocationId)
	if err != nil {
		return err
	}
	var selected FileSelection
	if err := a.db.WithContext(ctx).First(&selected, copy.FileID).Error; err != nil {
		return err
	}
	publication := &library.RestorePublication{Result: *result, Reconnect: selected.ReconnectVersionID == copy.FileVersionID,
		ParentID: selected.ParentID, Name: selected.Name,
		Original: &library.FileLocation{Size: size, Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(), Hash: hash}}
	if !damaged && !library.OnlineExcluded(location.Exclusions, result.Path) {
		keys, err := executor.ObserveTracking(location, full, info)
		if err != nil {
			return err
		}
		publication.Tracking = keys
	}
	result, err = a.exe.Lib().PublishRestore(ctx, publication)
	if err != nil {
		return err
	}
	return a.checkpointResult(ctx, copy, result)
}

func (a *jobRestoreRunner) checkpointResult(ctx context.Context, copy *Copy, result *library.RestoreResult) error {
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
		output := &Output{ItemID: copy.ItemID, Path: copy.TargetPath, Completed: true, ResultFileID: result.ResultFileID,
			Linked: result.ResultFileID != 0, Damaged: result.Outcome == library.RestoreDamaged,
			ActualHash: result.ActualHash, ActualSize: result.ActualSize, ResultMessage: message}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "item_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"completed", "result_file_id", "linked", "damaged", "actual_hash", "actual_size", "result_message"}),
		}).Create(output).Error; err != nil {
			return err
		}
		updated := tx.Model(&Copy{}).Where("item_id = ?", copy.ItemID).Update("status", entity.CopyStatus_COMPLETED)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			return fmt.Errorf("Restore item is missing, item_id=%d", copy.ItemID)
		}

		// Indexing owns its final checkpoint; execution completes when every distinct item is satisfied.
		var pending int64
		if err := tx.Model(&Copy{}).Where("status = ?", entity.CopyStatus_PENDING).Limit(1).Count(&pending).Error; err != nil {
			return err
		}
		if pending != 0 {
			return nil
		}
		return tx.Model(&executor.JobRecord{}).Where("id = ? AND status = ?", 1, entity.JobStatus_PENDING).
			Update("status", entity.JobStatus_COMPLETED).Error
	})
}

func (a *jobRestoreRunner) reconcileResults(ctx context.Context) error {
	// Library-only completion is recoverable without recopying bytes or re-running the binding decision.
	var after int64
	for {
		ids := a.db.Model(&Copy{}).Select("MIN(id)").Where("item_id > ? AND status = ?", after, entity.CopyStatus_PENDING).
			Group("item_id").Order("item_id").Limit(batchSize)
		var copies []Copy
		if err := a.db.WithContext(ctx).Where("id IN (?)", ids).Order("item_id").Find(&copies).Error; err != nil {
			return err
		}
		if len(copies) == 0 {
			return nil
		}
		for _, copy := range copies {
			result, err := a.restoreResult(ctx, &copy)
			if err != nil {
				return err
			}
			if result != nil {
				if err := a.checkpointResult(ctx, &copy, result); err != nil {
					return err
				}
			}
			after = copy.ItemID
		}
	}
}
