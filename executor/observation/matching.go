package observation

import (
	"bytes"
	"context"

	"github.com/samuelncui/yatm/entity"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type matcher struct {
	name       string
	candidates func(*gorm.DB, *Original) *gorm.DB
}

// Each complete global round precedes the next rule; later evidence never overrides a match.
var relocationMatchers = []matcher{
	{"path", func(db *gorm.DB, old *Original) *gorm.DB {
		if old.Path == "" {
			return nil
		}
		return db.Where("path = ?", old.Path)
	}},
	{"signature", func(db *gorm.DB, old *Original) *gorm.DB {
		if len(old.Signature) == 0 {
			return nil
		}
		return db.Where("signature = ?", old.Signature)
	}},
	{"native identity", func(db *gorm.DB, old *Original) *gorm.DB {
		key := old.Evidence
		if len(key.NativeKey) == 0 || key.NativeScope == "" {
			return nil
		}
		db = db.Where("native_scope = ? AND native_key = ?", key.NativeScope, key.NativeKey)
		if key.BirthNS != 0 {
			db = db.Where("birth_ns = ?", key.BirthNS)
		}
		if key.Generation != 0 {
			db = db.Where("generation = ?", key.Generation)
		}
		return db
	}},
	{"tracking UUID", func(db *gorm.DB, old *Original) *gorm.DB {
		if len(old.Evidence.UUID) == 0 || old.Evidence.UUIDScope == "" {
			return nil
		}
		return db.Where("uuid_scope = ? AND uuid = ?", old.Evidence.UUIDScope, old.Evidence.UUID)
	}},
}

// Match assigns existing File identities only after the caller stages the complete eligible scope.
// The caller excludes unobserved, inaccessible and otherwise ineligible original associations.
func Match(ctx context.Context, db *gorm.DB, logger logrus.FieldLogger) error {
	// Consume one old File and one current observation at a time in stable, bounded global rounds.
	for _, rule := range relocationMatchers {
		var after int64
		for {
			var old []*Original
			claimed := db.Model(&Item{}).Select("file_id").Where("file_id > 0")
			if err := db.WithContext(ctx).Where("file_id > ? AND file_id NOT IN (?)", after, claimed).
				Order("file_id").Limit(BatchSize).Find(&old).Error; err != nil {
				return err
			}
			if len(old) == 0 {
				break
			}
			for _, original := range old {
				query := rule.candidates(db.WithContext(ctx).Where("file_id = 0 AND independent = ? AND change != ?", false, entity.ScanChange_SCAN_CHANGE_REMOVED), original)
				if query == nil {
					continue
				}
				var item Item
				if err := query.Order("path").Limit(1).Find(&item).Error; err != nil {
					return err
				}
				if item.ID == 0 {
					continue
				}

				// A producer-generated opaque signature survives relocation when the observed content agrees.
				values := map[string]any{"file_id": original.FileID}
				if len(original.Signature) != 0 && len(original.Hash) == 32 && original.Size == item.Size && bytes.Equal(original.Hash, item.Hash) {
					values["signature"] = original.Signature
				}
				if err := db.WithContext(ctx).Model(&item).Updates(values).Error; err != nil {
					return err
				}
				if logger != nil {
					logger.WithFields(logrus.Fields{"file_id": original.FileID, "path": item.Path, "matched_by": rule.name}).Debug("matched original")
				}
			}
			after = old[len(old)-1].FileID
		}
	}
	return nil
}
