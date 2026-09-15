package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/config"
	"github.com/samuelncui/yatm/resource"
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
	db, err = openMigrationDB(conf, true)
	require.NoError(t, err)
	require.Error(t, db.Exec("INSERT INTO original VALUES (1)").Error)
	sqlDB, err = db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	after, err := os.ReadFile(conf.Database.DSN)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestInspectionRequiresCompleteBackupScope(t *testing.T) {
	root := t.TempDir()
	conf := &config.Config{Listen: ":8080"}
	conf.Database.Dialect = "sqlite"
	conf.Database.DSN = filepath.Join(root, "catalog.db")
	conf.Paths.Work = root
	conf.Preview.Root = filepath.Join(root, "previews")
	report, err := inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8080", report.ServerURL)
	require.NotEmpty(t, report.BackupPaths)

	conf.Paths.Work = t.TempDir()
	_, err = inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true)
	require.ErrorContains(t, err, "complete backup")
}

func TestBackupScopeRejectsExternalSymlink(t *testing.T) {
	root := t.TempDir()
	conf := &config.Config{Listen: ":8080"}
	conf.Database.Dialect = "sqlite"
	conf.Database.DSN = filepath.Join(root, "catalog.db")
	conf.Paths.Work = root
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "jobs")))
	_, err := inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true)
	require.ErrorContains(t, err, "external or unresolved link")
}

func TestBackupScopePrunesOnlyOwnedUpgradeArtifacts(t *testing.T) {
	// An arbitrary user directory occupying the reserved name blocks installation changes.
	root := t.TempDir()
	conf := &config.Config{Listen: ":8080"}
	conf.Database.Dialect = "sqlite"
	conf.Database.DSN = filepath.Join(root, "catalog.db")
	conf.Paths.Work = root
	upgrades := filepath.Join(root, ".yatm-upgrades")
	require.NoError(t, os.Mkdir(upgrades, 0o700))
	_, err := inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true)
	require.ErrorContains(t, err, "ownership marker")

	// Preserved older backups do not enter active-resource link checks or recursive backups.
	require.NoError(t, os.WriteFile(filepath.Join(upgrades, "OWNER"), []byte("yatm-installer-upgrades\n"), 0o600))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(upgrades, "old-link")))
	_, err = inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true)
	require.NoError(t, err)

	// Original content mixed into the active installation needs explicit operator classification.
	conf.Paths.Source = filepath.Join(root, "originals")
	require.NoError(t, os.Mkdir(conf.Paths.Source, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(conf.Paths.Source, "important.txt"), []byte("keep"), 0o600))
	_, err = inspectInstallation(context.Background(), nil, conf, filepath.Join(root, "config.yaml"), root, true)
	require.ErrorContains(t, err, "business files are mixed")
}

func TestFreshInspectionChecksUpgradeDirectoryOwnershipWithoutWrites(t *testing.T) {
	// A check must reject every ownership conflict that would block the real installation.
	for _, owner := range []string{"missing", "wrong", "symlink", "owned"} {
		t.Run(owner, func(t *testing.T) {
			root := t.TempDir()
			upgrades := filepath.Join(root, ".yatm-upgrades")
			if owner == "symlink" {
				require.NoError(t, os.Symlink(t.TempDir(), upgrades))
			} else {
				require.NoError(t, os.Mkdir(upgrades, 0o700))
			}
			if owner == "wrong" || owner == "owned" {
				marker := "someone-else\n"
				if owner == "owned" {
					marker = "yatm-installer-upgrades\n"
				}
				require.NoError(t, os.WriteFile(filepath.Join(upgrades, "OWNER"), []byte(marker), 0o600))
			}
			conf := &config.Config{Listen: ":9092"}
			conf.Database.Dialect = "sqlite"
			conf.Database.DSN = filepath.Join(root, "tapes.db")
			conf.Paths.Work = root

			// Read-only inspection neither creates a catalog nor repairs ownership metadata.
			_, err := inspectFreshInstallation(conf, root)
			if owner == "owned" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "reserved upgrade directory")
			}
			require.NoFileExists(t, conf.Database.DSN)
			entries, err := os.ReadDir(root)
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
}
