package legacy

import (
	"bytes"
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
)

func prepareArchivedVersions(ctx context.Context, db *gorm.DB, report *Report) error {
	// Build saved versions only from reconciled physical copy evidence.
	var after int64
	for {
		var copies []*stagedLibraryPosition
		if err := db.WithContext(ctx).Where("id > ? AND is_dir = ?", after, false).Order("id").Limit(256).Find(&copies).Error; err != nil {
			return err
		}
		if len(copies) == 0 {
			break
		}
		for _, copy := range copies {
			var file stagedLibraryFile
			if copy.FileID > 0 {
				if err := db.WithContext(ctx).Where("id = ?", copy.FileID).Limit(1).Find(&file).Error; err != nil {
					return err
				}
			}
			matches := file.ID != 0 && file.Size == copy.Size && (len(file.Hash) == 0 || bytes.Equal(file.Hash, copy.Hash))
			signature := file.Signature
			if !matches || len(signature) == 0 {
				signature, _ = library.NewFileSignature(copy.Hash, copy.Size)
			}
			if err := db.WithContext(ctx).Model(copy).Update("signature", signature).Error; err != nil {
				return err
			}
			if file.ID == 0 || len(signature) == 0 || file.Kind != entity.FileKind_FILE_KIND_REGULAR {
				continue
			}
			var existing stagedLibraryVersion
			if err := db.WithContext(ctx).Where("file_id = ? AND signature = ?", file.ID, signature).Limit(1).Find(&existing).Error; err != nil {
				return err
			}
			if existing.ID != 0 {
				if existing.Size != copy.Size || !bytes.Equal(existing.Hash, copy.Hash) {
					return fmt.Errorf("legacy archived facts disagree, File %d", file.ID)
				}
				continue
			}
			version := &stagedLibraryVersion{FileID: file.ID, Signature: signature, Hash: copy.Hash, Size: copy.Size, Mode: copy.Mode, MtimeNS: copy.ModTime.UnixNano()}
			if matches {
				version.Mode, version.MtimeNS = file.Mode, file.ModTime.UnixNano()
			}
			if err := db.WithContext(ctx).Create(version).Error; err != nil {
				return err
			}
		}
		after = copies[len(copies)-1].ID
	}

	// Isolated content facts remain recoverable from the complete legacy catalog backup.
	var uncertain int64
	versions := db.Model(&stagedLibraryVersion{}).Select("1").Where("file_versions_staging.file_id = files_staging.id")
	if err := db.WithContext(ctx).Model(&stagedLibraryFile{}).Where("kind = ? AND signature IS NOT NULL", entity.FileKind_FILE_KIND_REGULAR).
		Where("NOT EXISTS (?)", versions).Count(&uncertain).Error; err != nil {
		return err
	}
	if uncertain != 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d Files have no confirmed archived content; original facts remain in the complete legacy catalog backup, not fabricated FileVersions", uncertain))
	}
	return nil
}

// Strip transitional evidence only from prepared tables; legacy source and backup tables remain intact.
func finishLibraryStaging(ctx context.Context, db *gorm.DB) error {
	for _, column := range []string{"mode", "mod_time", "hash", "size", "signature"} {
		if db.Migrator().HasColumn(&stagedLibraryFile{}, column) {
			if err := db.WithContext(ctx).Migrator().DropColumn(&stagedLibraryFile{}, column); err != nil {
				return err
			}
		}
	}
	if db.Migrator().HasIndex(&stagedLibraryPosition{}, "idx_positions_file_id") {
		if err := db.Migrator().DropIndex(&stagedLibraryPosition{}, "idx_positions_file_id"); err != nil {
			return err
		}
	}
	if db.Migrator().HasColumn(&stagedLibraryPosition{}, "file_id") {
		return db.WithContext(ctx).Migrator().DropColumn(&stagedLibraryPosition{}, "file_id")
	}
	return nil
}
