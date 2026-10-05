package legacy

import (
	"bytes"
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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

		// Load each page's File facts once, without retaining the complete catalog.
		ids := make([]int64, 0, len(copies))
		for _, copy := range copies {
			if copy.FileID > 0 {
				ids = append(ids, copy.FileID)
			}
		}
		var files []*stagedLibraryFile
		if len(ids) > 0 {
			if err := db.WithContext(ctx).Where("id IN ?", ids).Find(&files).Error; err != nil {
				return fmt.Errorf("read archived File facts failed, %w", err)
			}
		}
		byID := make(map[int64]*stagedLibraryFile, len(files))
		for _, file := range files {
			byID[file.ID] = file
		}

		// Reconcile saved versions in physical-copy order, including duplicates within this page.
		type content struct {
			fileID    int64
			signature string
		}
		seen := make(map[content]*stagedLibraryVersion)
		versions := make([]*stagedLibraryVersion, 0, len(copies))
		for _, copy := range copies {
			file := byID[copy.FileID]
			matches := file != nil && file.Size == copy.Size && (len(file.Hash) == 0 || bytes.Equal(file.Hash, copy.Hash))
			var signature []byte
			if matches {
				signature = file.Signature
			}
			if len(signature) == 0 {
				signature, _ = library.NewFileSignature(copy.Hash, copy.Size)
			}
			copy.Signature = signature
			if file == nil || len(signature) == 0 || file.Kind != entity.FileKind_FILE_KIND_REGULAR {
				continue
			}
			key := content{fileID: file.ID, signature: string(signature)}
			existing := seen[key]
			if existing == nil {
				var stored stagedLibraryVersion
				if err := db.WithContext(ctx).Where("file_id = ? AND signature = ?", file.ID, signature).Limit(1).Find(&stored).Error; err != nil {
					return fmt.Errorf("read saved content for File %d failed, %w", file.ID, err)
				}
				if stored.ID != 0 {
					existing = &stored
					seen[key] = existing
				}
			}
			if existing != nil {
				if existing.Size != copy.Size || !bytes.Equal(existing.Hash, copy.Hash) {
					return fmt.Errorf("legacy archived facts disagree, File %d", file.ID)
				}
				continue
			}
			version := &stagedLibraryVersion{
				FileID: file.ID, Signature: signature, Hash: copy.Hash, Size: copy.Size, Mode: copy.Mode, MtimeNS: copy.MtimeNS,
			}
			if matches {
				version.Mode, version.MtimeNS = file.Mode, file.MtimeNS
			}
			seen[key] = version
			versions = append(versions, version)
		}

		// Commit bounded batches instead of syncing the database once per physical copy.
		if err := db.WithContext(ctx).Select("id", "signature").Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"signature"}),
		}).CreateInBatches(copies, 256).Error; err != nil {
			return fmt.Errorf("write migrated Position signatures failed, %w", err)
		}
		if len(versions) > 0 {
			if err := db.WithContext(ctx).CreateInBatches(versions, 256).Error; err != nil {
				return fmt.Errorf("write migrated saved versions failed, %w", err)
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
	// Remove source-only content fields after their saved versions have been reconciled.
	for _, column := range []string{"mode", "mtime_ns", "hash", "size", "signature"} {
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
		if err := db.WithContext(ctx).Migrator().DropColumn(&stagedLibraryPosition{}, "file_id"); err != nil {
			return err
		}
	}

	// SQLite rebuilds the staging table for a column drop and discards its indexes.
	for _, name := range []string{
		"idx_positions_signature", "idx_positions_media_path", "idx_positions_media_parent", "idx_positions_media_files",
		"idx_positions_health",
	} {
		if db.Migrator().HasIndex(&stagedLibraryPosition{}, name) {
			continue
		}
		if err := db.WithContext(ctx).Migrator().CreateIndex(&stagedLibraryPosition{}, name); err != nil {
			return fmt.Errorf("create migrated Position index %q failed, %w", name, err)
		}
	}
	return nil
}
