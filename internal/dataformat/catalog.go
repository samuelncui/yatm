// Package dataformat defines the first published Catalog and Job bundle formats.
package dataformat

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

const (
	CatalogRevision = 1
	CatalogTable    = "catalog_metadata"
)

var ErrUnsupportedCatalog = errors.New("unsupported Catalog format; use yatm-migrate for legacy data")

type catalogMetadata struct {
	ID       int    `gorm:"primaryKey;autoIncrement:false"`
	Format   string `gorm:"not null"`
	Revision int    `gorm:"not null"`
}

func (catalogMetadata) TableName() string { return CatalogTable }

// CheckCatalog inspects the marker without creating tables or adopting unmarked data.
func CheckCatalog(db *gorm.DB) (bool, error) {
	// An existing marker is authoritative, including when its revision is unsupported.
	if db.Migrator().HasTable(&catalogMetadata{}) {
		var rows []catalogMetadata
		if err := db.Limit(2).Find(&rows).Error; err != nil {
			return false, fmt.Errorf("read Catalog format failed, %w", err)
		}
		if len(rows) != 1 || rows[0].ID != 1 || rows[0].Format != "yatm-catalog" || rows[0].Revision != CatalogRevision {
			return false, ErrUnsupportedCatalog
		}
		for _, table := range []string{"library_settings", "file_operation_results"} {
			if db.Migrator().HasTable(table) {
				return false, fmt.Errorf("%w: incompatible pre-stable table %s", ErrUnsupportedCatalog, table)
			}
		}
		if db.Migrator().HasColumn("settings", "revision") {
			return false, fmt.Errorf("%w: incompatible pre-stable Settings layout", ErrUnsupportedCatalog)
		}
		if db.Migrator().HasTable("file_tracking_keys") {
			var found int
			if err := db.Table("file_tracking_keys").Select("1").Where("kind = ?", "yatm_uuid").Limit(1).Scan(&found).Error; err != nil {
				return false, fmt.Errorf("inspect obsolete tracking evidence failed, %w", err)
			}
			if found != 0 {
				return false, fmt.Errorf("%w: obsolete tracking UUID evidence requires explicit conversion or a fresh Catalog", ErrUnsupportedCatalog)
			}
		}
		return false, checkCatalogTimes(db)
	}

	// Missing Jobs alone does not make an existing Library or other database empty.
	tables, err := db.Migrator().GetTables()
	if err != nil {
		return false, fmt.Errorf("inspect Catalog tables failed, %w", err)
	}
	for _, table := range tables {
		if db.Dialector.Name() == "sqlite" && strings.HasPrefix(table, "sqlite_") {
			continue
		}
		return false, ErrUnsupportedCatalog
	}
	return true, nil
}

func checkCatalogTimes(db *gorm.DB) error {
	// A shared revision does not authorize AutoMigrate to reinterpret an older timestamp layout.
	for _, layout := range []struct {
		table string
		old   []string
		ns    []string
	}{
		{"files", []string{"created_at", "updated_at"}, []string{"created_at_ns", "updated_at_ns"}},
		{"locations", []string{"created_at", "updated_at", "last_sync_at"}, []string{"created_at_ns", "updated_at_ns", "last_sync_at_ns"}},
		{"media", []string{"create_time", "destroy_time"}, []string{"created_at_ns", "destroyed_at_ns"}},
		{"positions", []string{"mod_time", "write_time", "checked_at"}, []string{"mtime_ns", "written_at_ns", "checked_at_ns"}},
		{"file_versions", []string{"first_archived_at", "last_archived_at"}, []string{"first_archived_at_ns", "last_archived_at_ns"}},
		{"file_version_archives", []string{"archived_at"}, []string{"archived_at_ns"}},
		{"file_tracking_keys", []string{"observed_at"}, []string{"observed_at_ns"}},
		{"jobs", []string{"created_at", "updated_at", "deleted_at"}, []string{"created_at_ns", "updated_at_ns", "deleted_at_ns"}},
		{"settings", []string{"created_at", "updated_at"}, []string{"created_at_ns", "updated_at_ns"}},
	} {
		if !db.Migrator().HasTable(layout.table) {
			continue
		}
		types, err := db.Migrator().ColumnTypes(layout.table)
		if err != nil {
			return fmt.Errorf("inspect Catalog timestamps failed, table=%s, %w", layout.table, err)
		}
		columns := make(map[string]bool, len(types))
		for _, column := range types {
			columns[column.Name()] = true
		}
		for _, name := range layout.old {
			if columns[name] {
				return fmt.Errorf("%w: incompatible pre-stable timestamp column %s.%s", ErrUnsupportedCatalog, layout.table, name)
			}
		}
		for _, name := range layout.ns {
			if !columns[name] {
				return fmt.Errorf("%w: missing nanosecond column %s.%s", ErrUnsupportedCatalog, layout.table, name)
			}
		}
	}
	return nil
}

// InitializeCatalog establishes the marker only for an empty database.
func InitializeCatalog(db *gorm.DB) error {
	empty, err := CheckCatalog(db)
	if err != nil || !empty {
		return err
	}
	return MarkCatalog(db)
}

// MarkCatalog records the format after fresh initialization or an authorized legacy migration.
// The caller owns the transaction and must not use this to adopt an unsupported database.
func MarkCatalog(db *gorm.DB) error {
	if err := db.AutoMigrate(&catalogMetadata{}); err != nil {
		return fmt.Errorf("create Catalog format table failed, %w", err)
	}
	if err := db.Create(&catalogMetadata{ID: 1, Format: "yatm-catalog", Revision: CatalogRevision}).Error; err != nil {
		return fmt.Errorf("record Catalog format failed, %w", err)
	}
	return nil
}
