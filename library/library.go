package library

import (
	"context"
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	batchSize = 100
)

type Library struct {
	db     *gorm.DB
	online *onlineGate
}

func New(db *gorm.DB) *Library {
	return &Library{db: db, online: &onlineGate{sources: make(map[int64]struct{})}}
}

func (l *Library) AutoMigrate() error {
	// Reject unsupported stores before AutoMigrate can change their schema.
	if err := dataformat.InitializeCatalog(l.db); err != nil {
		return err
	}

	// Organization, originals and saved content have separate authoritative records.
	if err := l.db.AutoMigrate(ModelFile, ModelFileTag, ModelMedia, ModelPosition, &Location{},
		&FileLocation{}, &FileVersion{}, &FileVersionArchive{}, &FileTrackingKey{}, &LibrarySettings{}, &LocationMigration{}, &RestoreResult{}, &FileOperationResult{}); err != nil {
		return err
	}
	if err := ensurePositionIndexes(l.db); err != nil {
		return err
	}

	// Reconcile existing catalog facts before serving read-only version queries.
	return l.db.Transaction(func(tx *gorm.DB) error {
		return reconcileCoveredVersions(tx, tx)
	})
}

func ensurePositionIndexes(db *gorm.DB) error {
	migrationDB := db.Session(&gorm.Session{Logger: db.Logger.LogMode(logger.Silent)})
	indexes, err := migrationDB.Migrator().GetIndexes(ModelPosition)
	if err != nil {
		return fmt.Errorf("inspect Position indexes failed, %w", err)
	}
	existing := make(map[string]gorm.Index, len(indexes))
	for _, index := range indexes {
		existing[index.Name()] = index
	}
	specs := []struct {
		name    string
		columns string
		unique  bool
	}{
		{name: "idx_positions_media_path", columns: "media_id,path", unique: true},
		{name: "idx_positions_media_parent", columns: "media_id,parent_path,path"},
		{name: "idx_positions_media_files", columns: "media_id,is_dir,write_time"},
	}
	for _, spec := range specs {
		index, found := existing[spec.name]
		if found {
			unique, known := index.Unique()
			if strings.Join(index.Columns(), ",") == spec.columns && known && unique == spec.unique {
				continue
			}
			if err := migrationDB.Migrator().DropIndex(ModelPosition, spec.name); err != nil {
				return fmt.Errorf("replace Position index failed, index=%q, %w", spec.name, err)
			}
		}
		if err := migrationDB.Migrator().CreateIndex(ModelPosition, spec.name); err != nil {
			return fmt.Errorf("create Position index failed, index=%q, %w", spec.name, err)
		}
	}
	return nil
}

// Trim removes orphan inventory and unversioned Files. Version history requires explicit deletion.
func (l *Library) Trim(ctx context.Context, position, file bool) error {
	release, err := l.maintainOnline()
	if err != nil {
		return err
	}
	defer release()
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if position {
			media := tx.Model(ModelMedia).Select("1").Where("media.id = positions.media_id")
			if err := tx.Where("NOT EXISTS (?)", media).Delete(ModelPosition).Error; err != nil {
				return err
			}
		}
		if !file {
			return nil
		}
		var after int64
		for {
			var ids []int64
			online := tx.Model(&FileLocation{}).Select("1").Where("file_locations.file_id = files.id")
			versions := tx.Model(&FileVersion{}).Select("1").Where("file_versions.file_id = files.id")
			if err := tx.Model(ModelFile).Where("id > ? AND kind = ?", after, entity.FileKind_FILE_KIND_REGULAR).
				Where("NOT EXISTS (?)", online).Where("NOT EXISTS (?)", versions).Order("id").Limit(batchSize).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) == 0 {
				return nil
			}
			if err := deleteFileRows(ctx, tx, ids); err != nil {
				return err
			}
			after = ids[len(ids)-1]
		}
	})
}
