package library

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"gorm.io/gorm"
)

// countImportedPath resolves existing content and counts only newly required logical parents.
// A dry run records prospective nodes on disposable disk; negative IDs distinguish them from catalog IDs.
func countImportedPath(tx, planned *gorm.DB, logicalPath string) (bool, int64, error) {
	// Follow the same kind-aware collision names that actual admission creates or reuses.
	parts := strings.Split(logicalPath, "/")
	parentID, created := int64(0), int64(0)
	for index, name := range parts {
		if !utf8.ValidString(name) {
			return false, 0, fmt.Errorf("imported original component is not valid UTF-8")
		}
		directory := index < len(parts)-1
		kind := entity.FileKind_FILE_KIND_REGULAR
		if directory {
			kind = entity.FileKind_FILE_KIND_DIRECTORY
		}
		for suffix := 0; ; suffix++ {
			candidate := importedFileName(name, directory, suffix)
			var stored fileRow
			if parentID >= 0 {
				if err := tx.Where("parent_id = ? AND name = ?", parentID, candidate).Find(&stored).Error; err != nil {
					return false, 0, err
				}
			}
			if stored.ID == 0 && planned != nil {
				if err := planned.Where("parent_id = ? AND name = ?", parentID, candidate).Find(&stored).Error; err != nil {
					return false, 0, err
				}
				stored.ID = -stored.ID
			}
			if stored.ID != 0 {
				if stored.Kind != kind {
					continue
				}
				if !directory {
					return true, created, nil
				}
				parentID = stored.ID
				break
			}

			// Remember each new name across candidates without writing to the Library catalog.
			if planned != nil {
				stored = fileRow{ParentID: parentID, Name: candidate, Kind: kind}
				if err := planned.Create(&stored).Error; err != nil {
					return false, 0, err
				}
			}
			if !directory {
				return false, created, nil
			}
			created++
			parentID = -max(stored.ID, 1)
			break
		}
	}
	return false, created, nil
}

func openArchiveImportStage(ctx context.Context) (*gorm.DB, func() error, error) {
	// Reuse the existing File row schema in an isolated, request-owned naming workset.
	stage, err := resource.OpenTemporaryDB("", "yatm-archive-import-")
	if err != nil {
		return nil, nil, err
	}
	db := stage.DB.WithContext(ctx)
	if err := db.AutoMigrate(&fileRow{}); err != nil {
		return nil, nil, errors.Join(err, stage.Close())
	}
	return db, stage.Close, nil
}
