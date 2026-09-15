package library

import (
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openTestLibraryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "library.db"))
	require.NoError(t, err)
	return db
}

func newTestLibrary(t *testing.T) (*gorm.DB, *Library) {
	t.Helper()
	db := openTestLibraryDB(t)
	lib := New(db)
	require.NoError(t, lib.AutoMigrate())
	return db, lib
}
