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
		return false, nil
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
