package library

import (
	"fmt"

	"gorm.io/gorm"
)

func validateRestoreResultImport(tx *gorm.DB) error {
	// Historical Locations may be unregistered, but retained content owners cannot be guessed.
	var invalid int64
	if err := tx.Model(&RestoreResult{}).
		Joins("LEFT JOIN file_versions ON file_versions.id = restore_results.source_version_id").
		Joins("LEFT JOIN files ON files.id = restore_results.result_file_id").
		Where(`file_versions.id IS NULL OR file_versions.file_id != restore_results.source_file_id
			OR file_versions.signature IS NULL OR file_versions.signature != restore_results.signature
			OR file_versions.size != restore_results.expected_size
			OR file_versions.hash IS NULL OR file_versions.hash != restore_results.expected_hash
			OR (restore_results.result_file_id > 0 AND (files.id IS NULL OR files.kind != 0
				OR NOT EXISTS (SELECT 1 FROM file_versions AS result_version
				WHERE result_version.file_id = restore_results.result_file_id
				AND result_version.signature = restore_results.signature
				AND result_version.hash = restore_results.expected_hash
				AND result_version.size = restore_results.expected_size)))`).Count(&invalid).Error; err != nil {
		return fmt.Errorf("validate Restore result references failed, %w", err)
	}
	if invalid != 0 {
		return fmt.Errorf("invalid imported Restore result references (%d rows)", invalid)
	}
	return nil
}
