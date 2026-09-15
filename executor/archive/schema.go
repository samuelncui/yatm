package archive

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/executor"

	"gorm.io/gorm"
)

func ensureSchema(ctx context.Context, db *gorm.DB) error {
	// Existing bundles pass the shared format gate before their database is opened.
	if err := db.WithContext(ctx).AutoMigrate(&Config{}, &Item{}, &RawInput{}); err != nil {
		return fmt.Errorf("prepare Archive Job schema failed, %w", err)
	}
	return executor.InitSelectionObservations(db.WithContext(ctx))
}
