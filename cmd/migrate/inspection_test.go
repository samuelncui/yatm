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
