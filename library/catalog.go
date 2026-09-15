package library

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

func (l *Library) ListContentCopies(ctx context.Context, signature []byte, after int64, limit int) ([]*Position, bool, error) {
	if len(signature) == 0 || after < 0 || limit <= 0 || limit > 1000 {
		return nil, false, fmt.Errorf("invalid content copy page")
	}
	var values []*Position
	if err := l.db.WithContext(ctx).Where("signature = ? AND is_dir = ? AND id > ?", signature, false, after).
		Order("id").Limit(limit + 1).Find(&values).Error; err != nil {
		return nil, false, err
	}
	more := len(values) > limit
	if more {
		values = values[:limit]
	}
	return values, more, nil
}

func (l *Library) ListContentDuplicates(ctx context.Context, signature []byte, after int64, limit int) ([]*File, bool, error) {
	if len(signature) == 0 || after < 0 || limit <= 0 || limit > 1000 {
		return nil, false, fmt.Errorf("invalid duplicate content page")
	}
	db := l.db.WithContext(ctx)
	originals := db.Model(&FileLocation{}).Select("1").Where("file_locations.file_id = files.id AND signature = ?", signature)
	versions := db.Model(&FileVersion{}).Select("1").Where("file_versions.file_id = files.id AND signature = ?", signature)
	var files []*File
	if err := db.Where("id > ? AND (EXISTS (?) OR EXISTS (?))", after, originals, versions).Order("id").Limit(limit + 1).Find(&files).Error; err != nil {
		return nil, false, err
	}
	more := len(files) > limit
	if more {
		files = files[:limit]
	}
	ids := make([]int64, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.ID)
	}
	tags, err := l.mGetFileTags(ctx, db, ids...)
	if err != nil {
		return nil, false, err
	}
	for _, file := range files {
		file.Tags = tags[file.ID]
	}
	return files, more, nil
}

func (l *Library) FileState(ctx context.Context, fileID int64) (*entity.FileStateReply, error) {
	var result entity.FileStateReply
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var file File
		if err := tx.First(&file, fileID).Error; err != nil {
			return err
		}
		view := *l
		view.db = tx
		if err := view.HydrateFileContent(ctx, &file); err != nil {
			return err
		}
		result.Summary = file.ContentSummary
		var original FileLocation
		if err := tx.Where("file_id = ?", fileID).Limit(1).Find(&original).Error; err != nil {
			return err
		}
		var version FileVersion
		if err := tx.Where("file_id = ?", fileID).Order("last_archived_at DESC, id DESC").Limit(1).Find(&version).Error; err != nil {
			return err
		}
		if version.ID != 0 {
			result.LatestVersion = version.ToEntity()
		}
		if original.FileID == 0 {
			return nil
		}
		var location Location
		if err := tx.First(&location, original.LocationID).Error; err != nil {
			return err
		}
		result.Original, result.Location = original.ToEntity(), location.CatalogEntity()
		if len(original.Signature) == 0 {
			return nil
		}
		if err := tx.Model(ModelPosition).Where("signature = ? AND is_dir = ?", original.Signature, false).Count(&result.ArchivedCopies).Error; err != nil {
			return err
		}
		var healthCounts []struct {
			Health entity.PositionHealth
			Count  int64
		}
		if err := tx.Model(ModelPosition).Select("health, count(*) AS count").Where("signature = ? AND is_dir = ?", original.Signature, false).Group("health").Scan(&healthCounts).Error; err != nil {
			return err
		}
		for _, count := range healthCounts {
			switch count.Health {
			case entity.PositionHealth_HEALTHY:
				result.HealthyCopies += count.Count
			case entity.PositionHealth_POSITION_HEALTH_UNKNOWN:
				result.UncheckedCopies += count.Count
			default:
				result.UnhealthyCopies += count.Count
			}
		}
		result.Coverage = entity.ContentCoverage_NO_ARCHIVED_COPY
		if result.ArchivedCopies > 0 {
			result.Coverage = entity.ContentCoverage_ARCHIVED_CONTENT
		}
		return nil
	})
	return &result, err
}

// ImportArchivePositions explicitly creates independent catalog objects from physical inventory.
// Historical archive time is unknown; observing a copy is not a new backup operation.
func (l *Library) ImportArchivePositions(ctx context.Context, ids []int64) ([]int64, error) {
	ids = uniqueFileIDs(ids)
	if len(ids) == 0 || len(ids) > 1000 {
		return nil, fmt.Errorf("select between 1 and 1000 archive positions")
	}
	release, err := l.UseOnlineRead()
	if err != nil {
		return nil, err
	}
	defer release()
	var result []int64
	err = l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, id := range ids {
			var copy Position
			if err := tx.First(&copy, id).Error; err != nil {
				return err
			}
			if copy.IsDir || len(copy.Signature) == 0 {
				return fmt.Errorf("Position %d has no archived content identity", id)
			}
			var media Media
			if err := tx.First(&media, copy.MediaID).Error; err != nil {
				return err
			}
			name := media.Name
			if media.Kind == entity.MediaKind_MEDIA_KIND_TAPE || name == "" {
				name = media.Identity
			}
			if name == "." || name == ".." || strings.ContainsAny(name, "/\\") {
				return fmt.Errorf("invalid Media import directory %q", name)
			}
			file, err := createImportedFile(tx, path.Join("Unforged", name, copy.Path))
			if err != nil {
				return err
			}
			if _, err := recordVersion(tx, &FileVersion{FileID: file.ID, Signature: copy.Signature, Hash: copy.Hash, Size: copy.Size,
				Mode: copy.Mode, MtimeNS: copy.ModTime.UnixNano()}); err != nil {
				return err
			}
			result = append(result, file.ID)
		}
		return nil
	})
	return result, err
}

// ImportArchivedInventory is reserved for explicit inventory-import workflows and fixtures.
// Archive and Volume Scan must not invoke this to infer logical identity from signatures.
// Each invocation creates new organization; callers select this one-shot import explicitly.
func (l *Library) ImportArchivedInventory(ctx context.Context) error {
	var after int64
	for {
		var ids []int64
		if err := l.db.WithContext(ctx).Model(ModelPosition).Where("id > ? AND is_dir = ?", after, false).
			Where("signature IS NOT NULL").Order("id").Limit(batchSize).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		if _, err := l.ImportArchivePositions(ctx, ids); err != nil {
			return err
		}
		after = ids[len(ids)-1]
	}
}

// CreateArchiveFile allocates a raw input's logical identity before any physical transfer.
func (l *Library) CreateArchiveFile(ctx context.Context, targetPath string) (*File, error) {
	if err := entity.ValidateRelativePath(targetPath); err != nil {
		return nil, err
	}
	var result *File
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = createImportedFile(tx, path.Join("Unforged", "Archive", targetPath))
		return err
	})
	return result, err
}
