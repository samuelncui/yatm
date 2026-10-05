package dataformat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRemovedPreviewPreferencesAreRejectedWithoutWrites(t *testing.T) {
	// Emulate the former extension wire field without retaining an obsolete generated model.
	db, filename := openCatalog(t)
	require.NoError(t, InitializeCatalog(db))
	require.NoError(t, db.Model(&catalogMetadata{}).Where("id = 1").Update("revision", 1).Error)
	require.NoError(t, db.Exec("CREATE TABLE library_settings (id INTEGER PRIMARY KEY, preview BLOB)").Error)
	generator := &entity.PreviewGeneratorSettings{}
	generator.ProtoReflect().SetUnknown([]byte{0x0a, 0x03, 'p', 'n', 'g'})
	settings := &entity.PreviewSettings{Generators: []*entity.PreviewGeneratorSettings{generator}}
	encoded, err := settings.Value()
	require.NoError(t, err)
	require.NoError(t, db.Exec("INSERT INTO library_settings (id, preview) VALUES (1, ?)", encoded).Error)
	before, err := os.ReadFile(filename)
	require.NoError(t, err)

	// Preflight rejects this Alpha layout before schema or configuration migration can change it.
	_, err = CheckCatalog(db)
	require.ErrorIs(t, err, ErrUnsupportedCatalog)
	after, err := os.ReadFile(filename)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func openCatalog(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	filename := filepath.Join(t.TempDir(), "catalog.db")
	db, err := resource.OpenSQLite(filename)
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db, filename
}

func TestCatalogInitialization(t *testing.T) {
	// Inspection must not create the marker; only explicit fresh initialization does.
	db, filename := openCatalog(t)
	empty, err := CheckCatalog(db)
	require.NoError(t, err)
	require.True(t, empty)
	require.False(t, db.Migrator().HasTable(CatalogTable))
	require.NoError(t, InitializeCatalog(db))

	// Reopening the published family neither rewrites its marker nor consumes a revision.
	before, err := os.ReadFile(filename)
	require.NoError(t, err)
	require.NoError(t, InitializeCatalog(db))
	empty, err = CheckCatalog(db)
	require.NoError(t, err)
	require.False(t, empty)
	after, err := os.ReadFile(filename)
	require.NoError(t, err)
	require.Equal(t, before, after)
	var marker catalogMetadata
	require.NoError(t, db.First(&marker).Error)
	require.Equal(t, catalogMetadata{ID: 1, Format: "yatm-catalog", Revision: CatalogRevision}, marker)
}

func TestCatalogRejectsOldTimestampColumnsWithoutWrites(t *testing.T) {
	for _, test := range []struct {
		table   string
		columns string
	}{
		{"files", "created_at INTEGER, updated_at INTEGER"},
		{"locations", "created_at INTEGER, updated_at INTEGER, last_sync_at INTEGER"},
		{"media", "create_time DATETIME, destroy_time DATETIME"},
		{"positions", "mod_time DATETIME, write_time DATETIME, checked_at INTEGER"},
		{"file_versions", "first_archived_at INTEGER, last_archived_at INTEGER"},
		{"file_version_archives", "archived_at INTEGER"},
		{"file_tracking_keys", "kind TEXT, observed_at INTEGER"},
		{"jobs", "created_at INTEGER, updated_at INTEGER, deleted_at INTEGER"},
		{"settings", "created_at INTEGER, updated_at INTEGER"},
		{"files", "created_at_ns INTEGER, updated_at_ns INTEGER, created_at INTEGER"},
		{"files", "name TEXT"},
	} {
		t.Run(test.table+"/"+test.columns, func(t *testing.T) {
			// An older or mixed layout must not be silently filled with new zero timestamps.
			db, filename := openCatalog(t)
			require.NoError(t, InitializeCatalog(db))
			require.NoError(t, db.Exec("CREATE TABLE "+test.table+" (id INTEGER PRIMARY KEY, "+test.columns+")").Error)
			before, err := os.ReadFile(filename)
			require.NoError(t, err)
			_, err = CheckCatalog(db)
			require.ErrorIs(t, err, ErrUnsupportedCatalog)
			require.ErrorIs(t, InitializeCatalog(db), ErrUnsupportedCatalog)

			// Refusing the layout preserves the operator's original database byte for byte.
			after, err := os.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}

func TestCatalogAcceptsNanosecondColumns(t *testing.T) {
	// All current timestamp owners expose the same unit even while their tables initialize separately.
	db, filename := openCatalog(t)
	require.NoError(t, InitializeCatalog(db))
	for _, statement := range []string{
		"CREATE TABLE files (created_at_ns INTEGER, updated_at_ns INTEGER)",
		"CREATE TABLE locations (created_at_ns INTEGER, updated_at_ns INTEGER, last_sync_at_ns INTEGER)",
		"CREATE TABLE media (created_at_ns INTEGER, destroyed_at_ns INTEGER)",
		"CREATE TABLE positions (mtime_ns INTEGER, written_at_ns INTEGER, checked_at_ns INTEGER)",
		"CREATE TABLE file_versions (first_archived_at_ns INTEGER, last_archived_at_ns INTEGER)",
		"CREATE TABLE file_version_archives (archived_at_ns INTEGER)",
		"CREATE TABLE file_tracking_keys (kind TEXT, observed_at_ns INTEGER)",
		"CREATE TABLE jobs (created_at_ns INTEGER, updated_at_ns INTEGER, deleted_at_ns INTEGER)",
		"CREATE TABLE settings (created_at_ns INTEGER, updated_at_ns INTEGER)",
	} {
		require.NoError(t, db.Exec(statement).Error)
	}
	before, err := os.ReadFile(filename)
	require.NoError(t, err)
	empty, err := CheckCatalog(db)
	require.NoError(t, err)
	require.False(t, empty)

	// Admission performs no schema adoption or normalization on an accepted database.
	after, err := os.ReadFile(filename)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestUnsupportedCatalogPreservesBytes(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  []string
	}{
		{name: "Library without Jobs", sql: []string{"CREATE TABLE files (id INTEGER PRIMARY KEY, name TEXT)", "INSERT INTO files VALUES (1, 'kept')"}},
		{name: "unmarked Draft", sql: []string{"CREATE TABLE jobs (id INTEGER PRIMARY KEY, executor_id TEXT, revision INTEGER)"}},
		{name: "incompatible pre-stable settings", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'yatm-catalog', 1)", "CREATE TABLE library_settings (id INTEGER PRIMARY KEY, auto_collect_files BOOL)"}},
		{name: "incompatible pre-stable receipts", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'yatm-catalog', 1)", "CREATE TABLE file_operation_results (id INTEGER PRIMARY KEY, admitted_file_id INTEGER)"}},
		{name: "incompatible pre-stable Settings layout", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'yatm-catalog', 1)", "CREATE TABLE settings (key TEXT PRIMARY KEY, revision INTEGER)"}},
		{name: "obsolete tracking evidence", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'yatm-catalog', 1)", "CREATE TABLE file_tracking_keys (file_id INTEGER, kind TEXT)", "INSERT INTO file_tracking_keys VALUES (1, 'yatm_uuid')"}},
		{name: "later v1 revision", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'yatm-catalog', 2)"}},
		{name: "future revision", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'yatm-catalog', 999)"}},
		{name: "wrong family", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'other', 1)"}},
		{name: "empty marker", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)"}},
		{name: "multiple markers", sql: []string{"CREATE TABLE catalog_metadata (id INTEGER PRIMARY KEY, format TEXT, revision INTEGER)", "INSERT INTO catalog_metadata VALUES (1, 'yatm-catalog', 1), (2, 'yatm-catalog', 1)"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// The absence of Jobs or a recognizable column shape is not an empty Catalog.
			db, filename := openCatalog(t)
			for _, statement := range test.sql {
				require.NoError(t, db.Exec(statement).Error)
			}
			before, err := os.ReadFile(filename)
			require.NoError(t, err)
			_, err = CheckCatalog(db)
			require.ErrorIs(t, err, ErrUnsupportedCatalog)
			require.ErrorIs(t, InitializeCatalog(db), ErrUnsupportedCatalog)

			// Both read-only preflight and runtime admission leave all existing bytes intact.
			after, err := os.ReadFile(filename)
			require.NoError(t, err)
			require.Equal(t, before, after)
		})
	}
}
