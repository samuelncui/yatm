package executor

import (
	"context"
	"errors"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor/observation"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
)

// ConstrainCopiedObservations honors known copy provenance before generic identity matching.
// The caller holds the Location gate and has staged the complete eligible observation scope.
func (e *Executor) ConstrainCopiedObservations(ctx context.Context, db *gorm.DB, location *library.Location) error {
	return eachSelectionObservation(ctx, db, func(item *observation.Item) error {
		if item.CopyResult == nil {
			return nil
		}
		atPath, err := e.lib.GetFileLocationAtPath(ctx, location.ID, item.Path)
		if err != nil {
			return err
		}
		if atPath != nil {
			// COPY publication removed its stale target; later exact-path associations remain legitimate.
			return nil
		}
		independent, fileID := true, int64(0)
		if item.CopyResult.AdmittedFileID != nil {
			owner, err := e.lib.GetFile(ctx, *item.CopyResult.AdmittedFileID)
			if errors.Is(err, library.ErrFileNotFound) {
				return db.WithContext(ctx).Model(item).Updates(map[string]any{"independent": true, "file_id": int64(0)}).Error
			}
			if err != nil {
				return err
			}
			if owner.Kind != entity.FileKind_FILE_KIND_REGULAR {
				return db.WithContext(ctx).Model(item).Updates(map[string]any{"independent": true, "file_id": int64(0)}).Error
			}
			previous, err := e.lib.GetFileLocation(ctx, *item.CopyResult.AdmittedFileID)
			if err != nil {
				return err
			}
			if previous == nil || e.absentAdmissionPath(location, previous) {
				var claimed int64
				if err := db.WithContext(ctx).Model(&observation.Item{}).Where("file_id = ?", *item.CopyResult.AdmittedFileID).Count(&claimed).Error; err != nil {
					return err
				}
				if claimed == 0 {
					independent, fileID = false, *item.CopyResult.AdmittedFileID
				}
			}
		}
		return db.WithContext(ctx).Model(item).Updates(map[string]any{"independent": independent, "file_id": fileID}).Error
	})
}
