package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/library"
	"gorm.io/gorm"
)

// seedVersionDates gives the disposable review fixture distinct historical save observations.
func seedVersionDates(ctx context.Context, lib *library.Library, db *gorm.DB) error {
	// Content and copies already pass ordinary publication; only fixture dates are arranged here.
	for _, sample := range []struct {
		path string
		days []int
	}{
		{"Unforged/Documents/mutable.txt", []int{20, 29, 37}},
		{"Photos/archive-room.png", []int{22, 30, 39}},
	} {
		file, err := lib.GetByPath(ctx, library.Root.ID, sample.path)
		if err != nil {
			return err
		}
		if file == nil {
			return fmt.Errorf("missing version fixture %q", sample.path)
		}
		versions, more, err := lib.ListFileVersions(ctx, file.ID, 0, len(sample.days))
		if err != nil {
			return err
		}
		if more || len(versions) != len(sample.days) {
			return fmt.Errorf("unexpected version count for %q", sample.path)
		}

		// Endpoints and observations change together; restoration never uses the source modification date.
		if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			for index, version := range versions {
				stamp, err := dataformat.Nanoseconds(time.Date(
					2026, time.August, sample.days[index], 2, 0, 0, 0, time.UTC,
				))
				if err != nil {
					return fmt.Errorf("convert Demo archive date failed, %w", err)
				}
				if err := tx.Model(version).Updates(map[string]any{
					"first_archived_at_ns": stamp, "last_archived_at_ns": stamp,
				}).Error; err != nil {
					return err
				}
				if err := tx.Where("version_id = ?", version.ID).Delete(&library.FileVersionArchive{}).Error; err != nil {
					return err
				}
				if err := tx.Create(&library.FileVersionArchive{
					VersionID: version.ID, ArchivedAtNS: stamp,
				}).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
