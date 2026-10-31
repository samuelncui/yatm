package resource

import (
	"fmt"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func NewDBConn(dialect, dsn string) (*gorm.DB, error) {
	// Select the configured driver without publishing connection credentials.
	var dialector gorm.Dialector
	switch dialect {
	case "mysql":
		dialector = mysql.Open(dsn)
	case "sqlite":
		dialector = openSQLite(dsn)
	default:
		return nil, fmt.Errorf("unsupported database dialect %q", dialect)
	}

	// Connection errors retain their cause but never append the full DSN.
	db, err := gorm.Open(dialector)
	if err != nil {
		return nil, fmt.Errorf("open %s database failed, %w", dialect, err)
	}

	// Serialize SQLite writes to preserve the existing connection/locking contract.
	switch dialect {
	case "sqlite":
		sqlDB, err := db.DB()
		if err != nil {
			return nil, fmt.Errorf("configure sqlite database failed, %w", err)
		}
		sqlDB.SetMaxOpenConns(1)
	}
	return db, nil
}

func OpenSQLite(filename string) (*gorm.DB, error) {
	return NewDBConn("sqlite", filename)
}

type GORMScope func(db *gorm.DB) *gorm.DB
