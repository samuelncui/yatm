package archive

import (
	"context"
	"fmt"
	"os"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"gorm.io/gorm"
)

func (a *jobArchiveRunner) applySpec(ctx context.Context, spec *entity.ArchiveJobSpec) error {
	// Current jobs build their one manifest from registered selections.
	if spec != nil && len(spec.Selections) != 0 {
		return a.applyLibrarySpec(ctx, spec)
	}
	// Migrated jobs already carry their complete item manifest.
	var count int64
	if err := a.db.WithContext(ctx).Model(&Item{}).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("archive selections are empty")
	}
	return nil
}

func (a *jobArchiveRunner) resetManifest(ctx context.Context) error {
	if err := a.db.WithContext(ctx).
		Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Item{}).Error; err != nil {
		return fmt.Errorf("reset archive manifest failed, %w", err)
	}
	a.dropProgress()
	return nil
}

// captureRawInput also prepares unfinished legacy manifests, which did not carry frozen content identities.
func (a *jobArchiveRunner) captureRawInput(ctx context.Context, item *Item) error {
	// Legacy pending items acquire content facts once in their existing manifest.
	info, err := os.Lstat(item.Data.SourcePath)
	if err != nil {
		return err
	}
	expected, err := executor.HashObservedContent(ctx, item.Data.SourcePath, info)
	if err != nil {
		return err
	}
	// Allocate the logical File only after all ordinary input checks succeed.
	file, err := a.exe.Lib().CreateArchiveFile(ctx, item.TargetPath)
	if err != nil {
		return err
	}
	expected.FileId = file.ID
	item.Size = expected.SizeBytes
	item.Data.Expected = expected

	return nil
}
