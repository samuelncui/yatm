package resource

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func admitTestCatalog(*gorm.DB) error { return nil }

func TestCatalogRelativePathsOpenTheConfiguredFile(t *testing.T) {
	// The shipped configuration uses ./tapes.db; URI conversion must retain that local path.
	for _, test := range []struct {
		name string
		dsn  string
		path string
	}{
		{name: "plain", dsn: "catalog.db", path: "catalog.db"},
		{name: "dot", dsn: "./catalog.db", path: "catalog.db"},
		{name: "escaped", dsn: "sub dir/catalog #100%25.db", path: "sub dir/catalog #100%25.db"},
		{name: "explicit URI", dsn: "file:./catalog%20file.db?cache=private", path: "catalog file.db"},
	} {
		for _, wal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wal=%t", test.name, wal), func(t *testing.T) {
				t.Chdir(t.TempDir())
				require.NoError(t, os.MkdirAll(filepath.Dir(test.path), 0o700))
				catalog, err := OpenCatalog("sqlite", test.dsn, wal, admitTestCatalog)
				require.NoError(t, err)
				t.Cleanup(func() { require.NoError(t, catalog.Close()) })
				require.NoError(t, catalog.Write.Exec("CREATE TABLE example (value INTEGER)").Error)
				require.NoError(t, catalog.Write.Exec("INSERT INTO example VALUES (42)").Error)
				var value int
				require.NoError(t, catalog.Read.Raw("SELECT value FROM example").Scan(&value).Error)
				require.Equal(t, 42, value)
				require.FileExists(t, test.path)
			})
		}
	}
}

func TestCatalogDoubleSlashAbsolutePath(t *testing.T) {
	if filepath.Separator != '/' {
		t.Skip("Double-leading-slash local paths use POSIX filesystem semantics")
	}
	for _, wal := range []bool{false, true} {
		t.Run(fmt.Sprintf("wal=%t", wal), func(t *testing.T) {
			filename := "//" + strings.TrimPrefix(filepath.Join(t.TempDir(), "catalog.db"), "/")
			catalog, err := OpenCatalog("sqlite", filename, wal, admitTestCatalog)
			require.NoError(t, err)
			require.NoError(t, catalog.Close())
			require.FileExists(t, filename)
		})
	}
}

func TestCatalogJournalModeAndConnectionLifetime(t *testing.T) {
	// Reopening with false must undo persisted WAL, not just stop setting it.
	path := filepath.Join(t.TempDir(), "catalog with spaces.db")
	for _, wal := range []bool{false, true, true, false} {
		catalog, err := OpenCatalog("sqlite", path, wal, admitTestCatalog)
		require.NoError(t, err)
		writer, err := catalog.Write.DB()
		require.NoError(t, err)
		reader, err := catalog.Read.DB()
		require.NoError(t, err)
		require.Equal(t, 1, writer.Stats().MaxOpenConnections)
		var mode string
		require.NoError(t, catalog.Write.Raw("PRAGMA journal_mode").Scan(&mode).Error)
		if wal {
			require.Equal(t, "wal", mode)
			require.NotSame(t, writer, reader)
			require.Equal(t, catalogReaders, reader.Stats().MaxOpenConnections)
		} else {
			require.Equal(t, "delete", mode)
			require.Same(t, writer, reader)
		}
		require.NoError(t, catalog.Close())
		require.Error(t, writer.Ping())
		require.Error(t, reader.Ping())
	}
}

func TestCatalogWALReadersObserveCommittedDataDuringWrite(t *testing.T) {
	// Hold an uncommitted writer while all read connections access the old snapshot.
	catalog, err := OpenCatalog("sqlite", filepath.Join(t.TempDir(), "catalog.db"), true, admitTestCatalog)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, catalog.Close()) })
	require.NoError(t, catalog.Write.Exec("CREATE TABLE example (value INTEGER)").Error)
	require.NoError(t, catalog.Write.Exec("INSERT INTO example VALUES (1)").Error)
	tx := catalog.Write.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() { _ = tx.Rollback().Error })
	require.NoError(t, tx.Exec("UPDATE example SET value = 2").Error)

	// Connection-local configuration also applies to every lazily opened reader.
	readers, err := catalog.Read.DB()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < catalogReaders; i++ {
		conn, err := readers.Conn(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, conn.Close()) })
		var value, synchronous, busy int
		require.NoError(t, conn.QueryRowContext(ctx, "SELECT value FROM example").Scan(&value))
		require.Equal(t, 1, value)
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous))
		require.Equal(t, 2, synchronous)
		require.NoError(t, conn.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy))
		require.Equal(t, 5000, busy)
		_, err = conn.ExecContext(ctx, "UPDATE example SET value = 3")
		require.Error(t, err, "Catalog readers must be read-only at the driver boundary")
	}
	require.NoError(t, tx.Commit().Error)
	var value int
	require.NoError(t, catalog.Write.Raw("SELECT value FROM example").Scan(&value).Error)
	require.Equal(t, 2, value)
}

func TestCatalogRejectsFormatBeforeChangingJournalMode(t *testing.T) {
	// Rejection must not change an existing file's journal mode in either direction.
	path := filepath.Join(t.TempDir(), "catalog.db")
	for _, wal := range []bool{false, true} {
		catalog, err := OpenCatalog("sqlite", path, wal, admitTestCatalog)
		require.NoError(t, err)
		require.NoError(t, catalog.Close())
		unsupported := errors.New("unsupported test format")
		_, err = OpenCatalog("sqlite", path, !wal, func(*gorm.DB) error { return unsupported })
		require.ErrorIs(t, err, unsupported)

		db, err := OpenSQLite(path)
		require.NoError(t, err)
		var mode string
		require.NoError(t, db.Raw("PRAGMA journal_mode").Scan(&mode).Error)
		require.Equal(t, map[bool]string{false: "delete", true: "wal"}[wal], mode)
		require.NoError(t, closeDB(db))
	}
}

func TestCatalogWALConfigurationRejectsContradictions(t *testing.T) {
	for _, test := range []struct {
		dialect string
		dsn     string
		wal     bool
	}{
		{"mysql", "", true}, {"sqlite", ":memory:", true}, {"sqlite", "file:test?mode=memory", true},
		{"sqlite", "file:test?_journal_mode=WAL", false}, {"sqlite", "file:test?_journal=DELETE", true},
		{"sqlite", "file:test?_pragma=journal_mode(WAL)", false}, {"sqlite", "file:test?_synchronous=NORMAL", true},
		{"sqlite", "file:test?mode=ro", false}, {"sqlite", "file:test?immutable=1", true},
	} {
		t.Run(fmt.Sprintf("%s/%s/%t", test.dialect, test.dsn, test.wal), func(t *testing.T) {
			called := false
			_, err := OpenCatalog(test.dialect, test.dsn, test.wal, func(*gorm.DB) error { called = true; return nil })
			require.Error(t, err)
			require.False(t, called)
		})
	}

	// A matching DSN pragma is removed until the explicit post-validation mode switch.
	dsn, _, err := catalogSQLiteDSN("file:catalog.db?_journal_mode=WAL&_pragma=journal_mode(WAL)", true)
	require.NoError(t, err)
	_, query, _ := strings.Cut(dsn, "?")
	values, err := url.ParseQuery(query)
	require.NoError(t, err)
	require.Empty(t, values.Get("_journal_mode"))
	for _, value := range values["_pragma"] {
		require.NotContains(t, value, "journal_mode")
	}
}

func TestCatalogConnectionPolicyOverridesDSNTimeout(t *testing.T) {
	// Caller timeouts cannot disable the Catalog's bounded busy-wait policy.
	path := filepath.Join(t.TempDir(), "catalog.db")
	catalog, err := OpenCatalog("sqlite", path+"?_timeout=0&_busy_timeout=1&_pragma=busy_timeout(2)", true, admitTestCatalog)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, catalog.Close()) })
	for _, db := range []*gorm.DB{catalog.Write, catalog.Read} {
		var busy, synchronous int
		require.NoError(t, db.Raw("PRAGMA busy_timeout").Scan(&busy).Error)
		require.Equal(t, 5000, busy)
		require.NoError(t, db.Raw("PRAGMA synchronous").Scan(&synchronous).Error)
		require.Equal(t, 2, synchronous)
	}
}

func TestCatalogWriteContentionReturnsWithoutChangingTheOwner(t *testing.T) {
	// A second process cannot acquire the sole SQLite writer while an admitted write is open.
	path := filepath.Join(t.TempDir(), "catalog.db")
	catalog, err := OpenCatalog("sqlite", path, true, admitTestCatalog)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, catalog.Close()) })
	require.NoError(t, catalog.Write.Exec("CREATE TABLE example (value INTEGER)").Error)
	require.NoError(t, catalog.Write.Exec("INSERT INTO example VALUES (1)").Error)
	tx := catalog.Write.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, tx.Exec("UPDATE example SET value = 2").Error)
	other, err := OpenSQLite(path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, closeDB(other)) })
	// Use a short test-only wait; normal Catalog connections retain the policy tested above.
	require.NoError(t, other.Exec("PRAGMA busy_timeout=1").Error)
	require.ErrorContains(t, other.Exec("UPDATE example SET value = 3").Error, "locked")
	require.NoError(t, tx.Commit().Error)
	var value int
	require.NoError(t, catalog.Read.Raw("SELECT value FROM example").Scan(&value).Error)
	require.Equal(t, 2, value)
}

func TestSQLiteIncludesWALResetFix(t *testing.T) {
	// Both build-tag-selected drivers must embed an engine with the upstream fix.
	db, err := OpenSQLite(":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, closeDB(db)) }()
	var version string
	require.NoError(t, db.Raw("SELECT sqlite_version()").Scan(&version).Error)
	parts := strings.Split(version, ".")
	require.Len(t, parts, 3)
	var number int
	for _, part := range parts {
		component, err := strconv.Atoi(part)
		require.NoError(t, err)
		number = number*1000 + component
	}
	require.GreaterOrEqual(t, number, 3_051_003, "SQLite %s lacks the required WAL-reset fix", version)
	t.Logf("SQLite engine: %s", version)
}
