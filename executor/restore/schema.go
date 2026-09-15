package restore

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

func ensureSchema(ctx context.Context, db *gorm.DB) error {
	// Existing bundles pass the shared format gate before their database is opened.
	if err := db.WithContext(ctx).AutoMigrate(&Config{}, &Copy{}, &Output{}, &FileSelection{}); err != nil {
		return fmt.Errorf("prepare Restore Job schema failed, %w", err)
	}
	return nil
}
