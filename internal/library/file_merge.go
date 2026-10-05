package library

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RemoveFileVersion removes catalog history, leaving independent copies and Preview intact.
// A dry run verifies the same record and reports it without deleting anything.
func (l *Library) RemoveFileVersion(ctx context.Context, fileID, versionID int64, dryRun bool) (int64, error) {
	var removed int64
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Version and save observations must disappear atomically; a failed delete must preserve both.
		// Verify ownership before deleting either part of the saved history.
		var version FileVersion
		if err := tx.Where("id = ? AND file_id = ?", versionID, fileID).First(&version).Error; err != nil {
			return err
		}
		removed = 1
		if dryRun {
			return nil
		}
		return removeVersions(tx, tx.Model(&FileVersion{}).Where("id = ?", version.ID))
	})
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// MergeFiles combines a caller-owned source list in deterministic order.
func (l *Library) MergeFiles(ctx context.Context, targetID int64, sourceIDs []int64) error {
	// Preserve the existing slice interface while sharing the bounded mutation path.
	ids := uniqueFileIDs(sourceIDs)
	_, err := l.MergeFileMembers(ctx, targetID, func(yield func([]int64) error) error {
		for start := 0; start < len(ids); start += batchSize {
			if err := yield(ids[start:min(start+batchSize, len(ids))]); err != nil {
				return err
			}
		}
		return nil
	}, false)
	return err
}

// MergeFileMembers combines an ordered source stream in one metadata transaction.
// visit yields unique ascending source IDs in bounded batches and propagates yield errors.
func (l *Library) MergeFileMembers(ctx context.Context, targetID int64,
	visit func(yield func([]int64) error) error, dryRun bool) (int64, error) {
	if visit == nil {
		return 0, fmt.Errorf("merge requires a source visitor")
	}

	// A dry run validates the target and reports the sources this merge would retire.
	if dryRun {
		var target fileRow
		if err := l.readDB().WithContext(ctx).First(&target, targetID).Error; err != nil {
			return 0, err
		}
		if target.Kind != entity.FileKind_FILE_KIND_REGULAR {
			return 0, fmt.Errorf("merge target must be a regular File")
		}
		var merged int64
		if err := visit(func(ids []int64) error {
			merged += int64(len(ids))
			return nil
		}); err != nil {
			return 0, err
		}
		return merged, nil
	}

	// Version reassignment, source retirement and annotations must commit together on write failure.
	var merged int64
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row fileRow
		if err := tx.First(&row, targetID).Error; err != nil {
			return err
		}
		target := row.file()
		if target.Kind != entity.FileKind_FILE_KIND_REGULAR {
			return fmt.Errorf("merge target must be a regular File")
		}
		trash, err := l.newTrash(ctx, tx)
		if err != nil {
			return err
		}

		// Bounded metadata reads and shared placement preserve source order without per-File hydration.
		if err := visit(func(ids []int64) error {
			for start := 0; start < len(ids); start += batchSize {
				batch := ids[start:min(start+batchSize, len(ids))]
				stored, err := readFileRows(tx, batch...)
				if err != nil {
					return err
				}
				files := make([]*File, 0, len(batch))
				for _, id := range batch {
					source := stored[id]
					if source == nil {
						return ErrFileNotFound
					}
					if source.ID == target.ID || source.Kind != entity.FileKind_FILE_KIND_REGULAR {
						return fmt.Errorf("merge source must be a different regular File")
					}
					if _, err := l.mergeFileMetadata(ctx, tx, source, target); err != nil {
						return err
					}
					if err := mergeVersions(tx, source.ID, target.ID); err != nil {
						return err
					}
					files = append(files, source)
				}
				if err := placeInTrash(ctx, tx, trash.ID, files); err != nil {
					return err
				}
				if err := retireOriginals(tx, batch); err != nil {
					return err
				}
				merged += int64(len(batch))
			}
			return nil
		}); err != nil {
			return err
		}
		return saveFileRow(tx, target)
	})
	if err != nil {
		return 0, err
	}
	return merged, nil
}

func mergeVersions(tx *gorm.DB, sourceID, targetID int64) error {
	// Stream source history in stable order; source ordering chooses metadata deterministically.
	var after int64
	for {
		var versions []FileVersion
		if err := tx.Where("file_id = ? AND id > ?", sourceID, after).Order("id").Limit(batchSize).Find(&versions).Error; err != nil {
			return err
		}
		if len(versions) == 0 {
			return nil
		}
		for _, source := range versions {
			var target FileVersion
			if err := tx.Where("file_id = ? AND signature = ?", targetID, source.Signature).Limit(1).Find(&target).Error; err != nil {
				return err
			}
			if target.ID == 0 {
				source.FileID = targetID
				if err := tx.Save(&source).Error; err != nil {
					return err
				}
			} else if err := mergeVersion(tx, &source, &target); err != nil {
				return err
			}
			after = source.ID
		}
	}
}

func mergeVersion(tx *gorm.DB, source, target *FileVersion) error {
	// Signature is the merge identity; preserve the preferred version's content metadata unchanged.
	if err := recordVersionArchives(tx, target.ID, target.FirstArchivedAtNS, target.LastArchivedAtNS, source.FirstArchivedAtNS, source.LastArchivedAtNS); err != nil {
		return err
	}

	// Copy real save observations in bounded pages; never record the merge time as a save.
	var after int64
	hasAfter := false
	for {
		var observations []FileVersionArchive
		query := tx.Where("version_id = ?", source.ID)
		if hasAfter {
			query = query.Where("archived_at_ns > ?", after)
		}
		if err := query.Order("archived_at_ns").Limit(batchSize).Find(&observations).Error; err != nil {
			return err
		}
		if len(observations) == 0 {
			break
		}
		for i := range observations {
			observations[i].VersionID = target.ID
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&observations).Error; err != nil {
			return err
		}
		after = observations[len(observations)-1].ArchivedAtNS
		hasAfter = true
	}

	// Preserve the union's actual endpoints after all signed timestamp pages are copied.
	var endpoints struct {
		First *int64
		Last  *int64
	}
	if err := tx.Model(&FileVersionArchive{}).Select("MIN(archived_at_ns) AS first, MAX(archived_at_ns) AS last").Where("version_id = ?", target.ID).Scan(&endpoints).Error; err != nil {
		return err
	}
	target.FirstArchivedAtNS, target.LastArchivedAtNS = endpoints.First, endpoints.Last
	if err := tx.Save(target).Error; err != nil {
		return err
	}
	return removeVersions(tx, tx.Model(&FileVersion{}).Where("id = ?", source.ID))
}

// removeVersions deletes saved observations and their selected versions in the caller's transaction.
// Positions and Preview are independent; removing history never reconciles archive coverage.
func removeVersions(tx, versions *gorm.DB) error {
	if err := tx.Where("version_id IN (?)", versions.Select("id")).Delete(&FileVersionArchive{}).Error; err != nil {
		return err
	}
	return versions.Delete(&FileVersion{}).Error
}
