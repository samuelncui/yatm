package dataformat

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/internal/resource/resourcetest"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func openCatalog(t *testing.T) (*gorm.DB, func() string) {
	t.Helper()
	db, dsn := resourcetest.OpenCatalogDSN(t)
	return db, func() string { return catalogState(t, db, dsn) }
}

// catalogState captures every stored byte: the SQLite file, or each MySQL table's definition and
// checksum, so a test can prove an operation changed nothing.
func catalogState(t *testing.T, db *gorm.DB, dsn string) string {
	t.Helper()
	if db.Dialector.Name() == "sqlite" {
		data, err := os.ReadFile(dsn)
		require.NoError(t, err)
		return string(data)
	}
	tables, err := db.Migrator().GetTables()
	require.NoError(t, err)
	var state strings.Builder
	for _, table := range tables {
		var definition struct {
			Table       string `gorm:"column:Table"`
			CreateTable string `gorm:"column:Create Table"`
		}
		require.NoError(t, db.Raw("SHOW CREATE TABLE `"+table+"`").Scan(&definition).Error)
		var checksum struct {
			Table    string `gorm:"column:Table"`
			Checksum int64  `gorm:"column:Checksum"`
		}
		require.NoError(t, db.Raw("CHECKSUM TABLE `"+table+"`").Scan(&checksum).Error)
		fmt.Fprintf(&state, "%s\n%d\n", definition.CreateTable, checksum.Checksum)
	}
	return state.String()
}

func TestCatalogInitialization(t *testing.T) {
	// Inspection must not create the marker; only explicit fresh initialization does.
	db, state := openCatalog(t)
	empty, err := CheckCatalog(db)
	require.NoError(t, err)
	require.True(t, empty)
	require.False(t, db.Migrator().HasTable(CatalogTable))
	require.NoError(t, InitializeCatalog(db))

	// Reopening the published family neither rewrites its marker nor consumes a revision.
	before := state()
	require.NoError(t, InitializeCatalog(db))
	empty, err = CheckCatalog(db)
	require.NoError(t, err)
	require.False(t, empty)
	after := state()
	require.Equal(t, before, after)
	var marker catalogMetadata
	require.NoError(t, db.First(&marker).Error)
	require.Equal(t, catalogMetadata{ID: 1, Format: "yatm-catalog", Revision: CatalogRevision}, marker)
}

func TestCatalogAcceptsNanosecondColumns(t *testing.T) {
	// All current timestamp owners expose the same unit even while their tables initialize separately.
	db, state := openCatalog(t)
	require.NoError(t, InitializeCatalog(db))
	for _, statement := range []string{
		"CREATE TABLE files (created_at_ns INTEGER, updated_at_ns INTEGER)",
		"CREATE TABLE locations (created_at_ns INTEGER, updated_at_ns INTEGER, last_sync_at_ns INTEGER)",
		"CREATE TABLE media (created_at_ns INTEGER)",
		"CREATE TABLE positions (mtime_ns INTEGER, written_at_ns INTEGER, checked_at_ns INTEGER)",
		"CREATE TABLE file_versions (first_archived_at_ns INTEGER, last_archived_at_ns INTEGER)",
		"CREATE TABLE file_version_archives (archived_at_ns INTEGER)",
		"CREATE TABLE file_tracking_keys (kind TEXT, observed_at_ns INTEGER)",
		"CREATE TABLE jobs (created_at_ns INTEGER, updated_at_ns INTEGER, deleted_at_ns INTEGER)",
		"CREATE TABLE settings (created_at_ns INTEGER, updated_at_ns INTEGER)",
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	before := state()
	empty, err := CheckCatalog(db)
	require.NoError(t, err)
	require.False(t, empty)

	// Admission performs no schema adoption or normalization on an accepted database.
	after := state()
	require.Equal(t, before, after)
}

func TestUnsupportedCatalogPreservesBytes(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  []string
	}{
		{name: "Library without Jobs", sql: []string{"CREATE TABLE files (id INTEGER PRIMARY KEY, name TEXT)", "INSERT INTO files VALUES (1, 'kept')"}},
		{name: "unmarked Draft", sql: []string{"CREATE TABLE jobs (id INTEGER PRIMARY KEY, executor_id TEXT, revision INTEGER)"}},
		{name: "later v1 revision", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'yatm-catalog', 2)"}},
		{name: "future revision", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'yatm-catalog', 999)"}},
		{name: "wrong family", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'other', 1)"}},
		{name: "empty marker", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)"}},
		{name: "multiple markers", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'yatm-catalog', 1), (2, 'yatm-catalog', 1)"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// The absence of Jobs or a recognizable column shape is not an empty Catalog.
			db, state := openCatalog(t)
			for _, statement := range test.sql {
				require.NoError(t, db.Exec(statement).Error)
			}
			before := state()
			_, err := CheckCatalog(db)
			require.ErrorIs(t, err, ErrUnsupportedCatalog)
			require.ErrorIs(t, InitializeCatalog(db), ErrUnsupportedCatalog)

			// Both read-only preflight and runtime admission leave all existing bytes intact.
			after := state()
			require.Equal(t, before, after)
		})
	}
}
