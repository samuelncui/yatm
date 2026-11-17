// Package resourcetest opens disposable Catalog databases for tests.
package resourcetest

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// MySQLEnv names a MySQL server DSN without a database. When it is set, every Catalog test
// database is a new MySQL database on that server instead of a SQLite file.
const MySQLEnv = "YATM_TEST_MYSQL_DSN"

// Dialect reports the Catalog dialect OpenCatalog uses.
func Dialect() string {
	if os.Getenv(MySQLEnv) != "" {
		return "mysql"
	}
	return "sqlite"
}

// OpenCatalog opens an empty Catalog database owned by t through the production connection
// settings, and closes and removes it when t ends.
func OpenCatalog(t testing.TB) *gorm.DB {
	t.Helper()
	db, _ := OpenCatalogDSN(t)
	return db
}

// Reopen opens another connection pool to a database OpenCatalogDSN created.
func Reopen(t testing.TB, dsn string) *gorm.DB {
	t.Helper()
	db, err := resource.NewDBConn(Dialect(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resource.CloseDB(db) })
	return db
}

// OpenCatalogDSN also returns the DSN, so a test can open more connections to the same database.
func OpenCatalogDSN(t testing.TB) (*gorm.DB, string) {
	t.Helper()
	server := os.Getenv(MySQLEnv)
	if server == "" {
		dsn := filepath.Join(t.TempDir(), "catalog.db")
		db, err := resource.OpenSQLite(dsn)
		require.NoError(t, err)
		t.Cleanup(func() { _ = resource.CloseDB(db) })
		return db, dsn
	}

	// Create a uniquely named database with the required collation on the shared server.
	cfg, err := mysqldriver.ParseDSN(server)
	require.NoError(t, err)
	suffix := make([]byte, 8)
	_, err = rand.Read(suffix)
	require.NoError(t, err)
	cfg.DBName = "yatm_test_" + hex.EncodeToString(suffix)
	admin, err := gorm.Open(mysql.Open(server))
	require.NoError(t, err)
	require.NoError(t, admin.Exec("CREATE DATABASE `"+cfg.DBName+"` CHARACTER SET utf8mb4 COLLATE "+
		resource.MySQLCollation).Error)
	t.Cleanup(func() {
		_ = admin.Exec("DROP DATABASE IF EXISTS `" + cfg.DBName + "`").Error
		_ = resource.CloseDB(admin)
	})

	// Open the database as the service does; cleanups run in reverse, so it closes before the drop.
	dsn := cfg.FormatDSN()
	db, err := resource.NewDBConn("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resource.CloseDB(db) })
	return db, dsn
}
