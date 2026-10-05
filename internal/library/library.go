package library

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"gorm.io/gorm"
)

const (
	batchSize = 100
)

type Library struct {
	db                *gorm.DB
	reader            *gorm.DB
	settings          *settingspkg.Module
	identicalTempRoot string
}

func New(db *gorm.DB) *Library {
	return NewWithSettings(db, settingspkg.New(db, settingspkg.PreviewDefinition{}))
}

// NewWithSettings binds Library request preferences to the process Settings module.
func NewWithSettings(db *gorm.DB, settings *settingspkg.Module) *Library {
	return &Library{db: db, settings: settings}
}

// NewWithCatalog uses the Catalog's explicit read and write connection owners.
func NewWithCatalog(write, read *gorm.DB, settings *settingspkg.Module) *Library {
	return &Library{db: write, reader: read, settings: settings}
}

// readDB keeps transaction-scoped Library values on their transaction handle.
func (l *Library) readDB() *gorm.DB {
	if l.reader != nil {
		return l.reader
	}
	return l.db
}

// Settings returns the process module shared by Library consumers.
func (l *Library) Settings() *settingspkg.Module { return l.settings }

func (l *Library) AutoMigrate() error {
	// Reject unsupported stores before AutoMigrate can change their schema.
	if err := dataformat.InitializeCatalog(l.db); err != nil {
		return err
	}
	if l.db.Migrator().HasTable(&Location{}) {
		if !l.db.Migrator().HasColumn(&Location{}, "config") {
			return fmt.Errorf("Location configuration layout requires one-time conversion before startup")
		}
		var missing int64
		if err := l.db.Model(&Location{}).Where("config IS NULL OR config = 'null'").Count(&missing).Error; err != nil {
			return err
		}
		if missing > 0 {
			return fmt.Errorf("%d Locations require one-time configuration conversion before startup", missing)
		}
	}

	// Organization, originals and saved content have separate authoritative records.
	if err := l.db.AutoMigrate(ModelFile, ModelFileTag, ModelMedia, ModelPosition, &Location{},
		&FileLocation{}, &FileVersion{}, &FileVersionArchive{}, &FileTrackingKey{}); err != nil {
		return err
	}
	if err := l.settings.AutoMigrate(); err != nil {
		return err
	}
	return nil
}

// TrimResult reports the rows one trim removed, or would remove under a dry run.
type TrimResult struct {
	Positions int64
	Files     int64
}

// Trim removes orphan inventory and unversioned Files. Version history requires explicit deletion.
// A dry run counts the same rows without deleting any of them.
func (l *Library) Trim(ctx context.Context, position, file, dryRun bool) (*TrimResult, error) {
	result := new(TrimResult)
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if position {
			orphans := func() *gorm.DB {
				media := tx.Model(ModelMedia).Select("1").Where("media.id = positions.media_id")
				return tx.Model(ModelPosition).Where("NOT EXISTS (?)", media)
			}
			if dryRun {
				if err := orphans().Count(&result.Positions).Error; err != nil {
					return err
				}
			} else if deleted := orphans().Delete(ModelPosition); deleted.Error != nil {
				return deleted.Error
			} else {
				result.Positions = deleted.RowsAffected
			}
		}
		if !file {
			return nil
		}

		// Page by immutable File identity so a dry run and the real trim walk the same rows.
		var after int64
		for {
			var ids []int64
			originals := tx.Model(&FileLocation{}).Select("1").Where("file_locations.file_id = files.id")
			versions := tx.Model(&FileVersion{}).Select("1").Where("file_versions.file_id = files.id")
			if err := tx.Model(ModelFile).Where("id > ? AND kind = ?", after, entity.FileKind_FILE_KIND_REGULAR).
				Where("NOT EXISTS (?)", originals).Where("NOT EXISTS (?)", versions).
				Order("id").Limit(batchSize).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				return nil
			}
			result.Files += int64(len(ids))
			if !dryRun {
				if err := deleteFileRows(ctx, tx, ids); err != nil {
					return err
				}
			}
			after = ids[len(ids)-1]
		}
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}
