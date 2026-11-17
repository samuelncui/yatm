package resource

import (
	"errors"
	"fmt"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const (
	// MySQLCollation is the only accepted Catalog database collation. It compares code points, so
	// name and path equality, uniqueness and paging order match SQLite's byte comparison.
	MySQLCollation = "utf8mb4_bin"

	mysqlConnections = 16
	mysqlConnLife    = 5 * time.Minute
)

func NewDBConn(dialect, dsn string) (*gorm.DB, error) {
	// Select the configured driver without publishing connection credentials.
	var dialector gorm.Dialector
	switch dialect {
	case "mysql":
		value, err := mysqlDSN(dsn)
		if err != nil {
			return nil, err
		}
		dialector = mysql.Open(value)
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
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("configure %s database failed, %w", dialect, err)
	}
	switch dialect {
	case "sqlite":
		// Serialize SQLite writes to preserve the existing connection/locking contract.
		sqlDB.SetMaxOpenConns(1)
	case "mysql":
		// MySQL reads and writes share one pool; concurrent write transactions repeat after a deadlock.
		sqlDB.SetMaxOpenConns(mysqlConnections)
		sqlDB.SetMaxIdleConns(mysqlConnections)
		sqlDB.SetConnMaxLifetime(mysqlConnLife)
		if err := checkMySQLCollation(db); err != nil {
			_ = sqlDB.Close()
			return nil, err
		}
	}
	return db, nil
}

// mysqlDSN fixes the session options the Catalog depends on: utf8mb4_bin literals, READ COMMITTED
// isolation, which avoids gap locks on ordinary inserts, affected rows that count matched rows as
// SQLite does, and parsed DATETIME values, which only the v0.1.x import reads.
func mysqlDSN(dsn string) (string, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return "", errors.New("invalid MySQL Catalog DSN")
	}
	if cfg.DBName == "" {
		return "", errors.New("the MySQL Catalog DSN must name a database")
	}
	cfg.Collation = MySQLCollation
	cfg.ClientFoundRows = true
	cfg.ParseTime = true
	if cfg.Params == nil {
		cfg.Params = make(map[string]string)
	}
	delete(cfg.Params, "charset")
	cfg.Params["transaction_isolation"] = "'READ-COMMITTED'"
	return cfg.FormatDSN(), nil
}

// checkMySQLCollation refuses a database whose default collation would create Catalog tables that
// treat case- or accent-differing names as equal.
func checkMySQLCollation(db *gorm.DB) error {
	var collation string
	if err := db.Raw("SELECT @@collation_database").Scan(&collation).Error; err != nil {
		return fmt.Errorf("read MySQL database collation failed, %w", err)
	}
	if !strings.EqualFold(collation, MySQLCollation) {
		return fmt.Errorf("MySQL Catalog database collation is %s, expected %s", collation, MySQLCollation)
	}
	return nil
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
