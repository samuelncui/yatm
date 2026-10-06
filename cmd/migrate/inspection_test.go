package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/internal/config"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestReadOnlyInspectionDoesNotCreateOrWriteDatabase(t *testing.T) {
	dir := t.TempDir()
	conf := &config.Config{}
	conf.Database.Dialect = "sqlite"
	conf.Database.DSN = filepath.Join(dir, "catalog.db")
	db, err := openMigrationDB(conf, true)
	require.NoError(t, err)
	require.Nil(t, db)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries)

	writable, err := resource.OpenSQLite(conf.Database.DSN)
	require.NoError(t, err)
	require.NoError(t, writable.Exec("CREATE TABLE original (id INTEGER)").Error)
	sqlDB, err := writable.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	before, err := os.ReadFile(conf.Database.DSN)
	require.NoError(t, err)
	sidecars := make(map[string][]byte)
	for _, suffix := range []string{"-wal", "-shm"} {
		data, readErr := os.ReadFile(conf.Database.DSN + suffix)
		if readErr == nil {
			sidecars[suffix] = data
		} else {
			require.ErrorIs(t, readErr, os.ErrNotExist)
		}
	}
	db, err = openMigrationDB(conf, true)
	require.NoError(t, err)
	require.Error(t, db.Exec("INSERT INTO original VALUES (1)").Error)
	sqlDB, err = db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	after, err := os.ReadFile(conf.Database.DSN)
	require.NoError(t, err)
	require.Equal(t, before, after)
	for _, suffix := range []string{"-wal", "-shm"} {
		data, readErr := os.ReadFile(conf.Database.DSN + suffix)
		if expected, existed := sidecars[suffix]; existed {
			require.NoError(t, readErr)
			require.Equal(t, expected, data)
		} else {
			require.ErrorIs(t, readErr, os.ErrNotExist, "read-only inspection must not create SQLite sidecars")
		}
	}
}

func TestInspectionRequiresCompleteCurrentBundles(t *testing.T) {
	// A current catalog with missing bundles must be recovered, not admitted as a resumable upgrade.
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "catalog.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, dataformat.MarkCatalog(db))
	require.NoError(t, db.Exec(`CREATE TABLE jobs (
		id INTEGER PRIMARY KEY,
		created_at_ns INTEGER NOT NULL DEFAULT 0,
		updated_at_ns INTEGER NOT NULL DEFAULT 0,
		deleted_at_ns INTEGER NOT NULL DEFAULT 0
	)`).Error)
	require.NoError(t, db.Exec("INSERT INTO jobs (id) VALUES (1)").Error)

	// Every normal inspection enforces the complete current storage contract.
	conf := &config.Config{Listen: ":8080"}
	conf.Paths.Work = t.TempDir()
	_, err = inspectInstallation(context.Background(), db, conf, "", "", true, 0)
	require.ErrorIs(t, err, dataformat.ErrBundleMissing)
}

func TestInspectionRequiresCompleteBackupScope(t *testing.T) {
	// Contained deployment resources fit in the complete installation archive.
	root := t.TempDir()
	t.Chdir(root)
	conf := &config.Config{Listen: ":8080"}
	conf.Database.Dialect = "sqlite"
	conf.Database.DSN = filepath.Join(root, "catalog.db")
	conf.Paths.Work = root
	conf.Preview.Root = filepath.Join(root, "previews")
	report, err := inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true, 0)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8080", report.ServerURL)
	require.NotEmpty(t, report.BackupPaths)

	// An external work root requires a coordinated manual backup.
	conf.Paths.Work = t.TempDir()
	_, err = inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true, 0)
	require.ErrorContains(t, err, "complete backup")

	// Required resources cannot live in the subtree intentionally excluded from backups.
	conf.Paths.Work = filepath.Join(root, ".backup", "jobs")
	_, err = inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true, 0)
	require.ErrorContains(t, err, "complete backup")
}

func TestBackupScopeRejectsExternalSymlink(t *testing.T) {
	// Links retain their names in tar, but cannot stand in for required external contents.
	root := t.TempDir()
	t.Chdir(root)
	conf := &config.Config{Listen: ":8080"}
	conf.Database.Dialect = "sqlite"
	conf.Database.DSN = filepath.Join(root, "catalog.db")
	conf.Paths.Work = root
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "jobs")))
	_, err := inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true, 0)
	require.ErrorContains(t, err, "external or unresolved link")
}

func TestBackupScopeRejectsAbsoluteInternalSymlinkWithoutWrites(t *testing.T) {
	// An absolute link works in the installation but still points there after backup extraction.
	root := t.TempDir()
	t.Chdir(root)
	conf := &config.Config{Listen: ":8080"}
	conf.Database.Dialect = "sqlite"
	conf.Database.DSN = filepath.Join(root, "catalog.db")
	conf.Paths.Work = root
	require.NoError(t, os.Mkdir(filepath.Join(root, "storage"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "storage", "keep"), []byte("original"), 0o600))
	require.NoError(t, os.Symlink(filepath.Join(root, "storage"), filepath.Join(root, "jobs")))

	// Read-only preflight must reject this before a backup or any migration work is created.
	_, err := inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true, 0)
	require.ErrorContains(t, err, "absolute link")
	require.NoFileExists(t, conf.Database.DSN)
	require.NoDirExists(t, filepath.Join(root, ".backup"))
	data, err := os.ReadFile(filepath.Join(root, "jobs", "keep"))
	require.NoError(t, err)
	require.Equal(t, "original", string(data))
}

func TestBackupScopeAcceptsRelativeContainedSymlinkChain(t *testing.T) {
	// Relative links remain inside the installation when its complete archive is relocated.
	root := t.TempDir()
	t.Chdir(root)
	conf := &config.Config{Listen: ":8080"}
	conf.Database.Dialect = "sqlite"
	conf.Database.DSN = filepath.Join(root, "catalog.db")
	conf.Paths.Work = filepath.Join(root, "jobs")
	require.NoError(t, os.Mkdir(filepath.Join(root, "storage"), 0o700))
	require.NoError(t, os.Symlink("storage", filepath.Join(root, "current")))
	require.NoError(t, os.Symlink("current", conf.Paths.Work))

	// Previously retained backups remain outside active-resource inspection.
	require.NoError(t, os.Mkdir(filepath.Join(root, ".backup"), 0o700))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, ".backup", "old-link")))
	_, err := inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true, 0)
	require.NoError(t, err)

	// A relative link into an excluded backup is not contained in the new archive.
	require.NoError(t, os.Symlink(".backup", filepath.Join(root, "excluded")))
	_, err = inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true, 0)
	require.ErrorContains(t, err, "excluded backup directory")
}

func TestBackupScopeExcludesOnlyBackupDirectory(t *testing.T) {
	// Previously retained backups do not enter active-resource checks or recursive backups.
	root := t.TempDir()
	t.Chdir(root)
	conf := &config.Config{Listen: ":8080"}
	conf.Database.Dialect = "sqlite"
	conf.Database.DSN = filepath.Join(root, "catalog.db")
	conf.Paths.Work = root
	upgrades := filepath.Join(root, ".backup")
	require.NoError(t, os.Mkdir(upgrades, 0o700))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(upgrades, "old-link")))
	_, err := inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true, 0)
	require.NoError(t, err)

	// The former installer directory is part of the full archive and is checked normally.
	old := filepath.Join(root, ".yatm-upgrades")
	require.NoError(t, os.Mkdir(old, 0o700))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(old, "external")))
	_, err = inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true, 0)
	require.ErrorContains(t, err, "external or unresolved link")
	require.NoError(t, os.Remove(filepath.Join(old, "external")))

	// Original content mixed into the active installation needs explicit operator classification.
	conf.Paths.Source = filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(conf.Paths.Source, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(conf.Paths.Source, "important.txt"), []byte("keep"), 0o600))
	_, err = inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true, 0)
	require.ErrorContains(t, err, "business files are mixed")
}

func TestFreshInspectionChecksBackupDirectoryWithoutWrites(t *testing.T) {
	// Read-only inspection accepts a real backup directory, never a link or ordinary file.
	for _, kind := range []string{"directory", "file", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			// Construct the reserved path without invoking installer mutations.
			root := t.TempDir()
			upgrades := filepath.Join(root, ".backup")
			switch kind {
			case "symlink":
				require.NoError(t, os.Symlink(t.TempDir(), upgrades))
			case "file":
				require.NoError(t, os.WriteFile(upgrades, []byte("keep"), 0600))
			default:
				require.NoError(t, os.Mkdir(upgrades, 0o700))
			}
			conf := &config.Config{Listen: ":9092"}
			conf.Database.Dialect = "sqlite"
			conf.Database.DSN = filepath.Join(root, "tapes.db")
			conf.Paths.Work = root

			// Read-only inspection neither creates a catalog nor repairs ownership metadata.
			_, err := inspectFreshInstallation(conf, root)
			if kind == "directory" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "reserved backup directory")
			}
			require.NoFileExists(t, conf.Database.DSN)
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
}
