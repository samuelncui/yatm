package resource

import (
	"fmt"
	"strings"

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

// OpenWALSQLite opens a writable SQLite file in WAL mode, so a commit costs one fsync and
// readers proceed alongside the writer. The mode persists in the file; its -wal and -shm
// sidecars belong to it.
func OpenWALSQLite(filename string) (*gorm.DB, error) {
	db, err := OpenSQLite(filename)
	if err != nil {
		return nil, err
	}
	var mode string
	if err := db.Raw("PRAGMA journal_mode=WAL").Scan(&mode).Error; err != nil || !strings.EqualFold(mode, "wal") {
		_ = CloseDB(db)
		return nil, fmt.Errorf("set SQLite journal mode to WAL failed, mode=%q, %w", mode, err)
	}
	return db, nil
}
