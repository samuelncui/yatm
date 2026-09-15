package legacy

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/internal/dataformat"
	legacypb "github.com/samuelncui/yatm/migrate/legacy/pb"
	"gorm.io/gorm"
)

type Schema string

const (
	SchemaEmpty   Schema = "empty"
	SchemaLegacy  Schema = "legacy"
	SchemaCurrent Schema = "current"
)

// DetectSchema identifies the active Job catalog format without changing it.
func DetectSchema(db *gorm.DB) (Schema, error) {
	// Published metadata wins over column names, including unsupported future revisions.
	empty, err := dataformat.CheckCatalog(db)
	if err == nil {
		if empty {
			return SchemaEmpty, nil
		}
		return SchemaCurrent, nil
	}
	if db.Migrator().HasTable(dataformat.CatalogTable) {
		return "", err
	}

	// Only the released legacy catalog has an explicit offline adapter.
	if !db.Migrator().HasColumn("jobs", "executor_id") &&
		db.Migrator().HasColumn("jobs", "state") && db.Migrator().HasColumn("jobs", "status") {
		return SchemaLegacy, nil
	}
	return "", err
}

// RunningJobIDs returns legacy Jobs that are inside a physical execution attempt.
func RunningJobIDs(ctx context.Context, db *gorm.DB) ([]int64, error) {
	schema, err := DetectSchema(db)
	if err != nil {
		return nil, err
	}
	if schema != SchemaLegacy {
		return nil, nil
	}

	var ids []int64
	if err := db.WithContext(ctx).Table("jobs").
		Where("status = ?", legacypb.JobStatus_PROCESSING).
		Order("id").Pluck("id", &ids).Error; err != nil {
		return nil, fmt.Errorf("query running legacy Jobs failed, %w", err)
	}
	return ids, nil
}
