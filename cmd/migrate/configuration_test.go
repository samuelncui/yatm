package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestConfigurationPlanAppliesAfterLegacyMigrationCreatesEmptySettingsSchema(t *testing.T) {
	// Legacy review has no settings tables; catalog migration does not create preference rows.
	ctx, root := context.Background(), t.TempDir()
	t.Chdir(root)
	require.NoError(t, os.Mkdir("source", 0o755))
	db, err := resource.OpenSQLite(filepath.Join(root, "catalog.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	filename := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(filename, []byte("paths:\n  source: ./source\n"), 0o640))
	plan, err := planConfiguration(ctx, db, filename)
	require.NoError(t, err)
	require.False(t, db.Migrator().HasTable(&library.Location{}))

	// Activating the catalog and creating empty tables must not invalidate user consent.
	require.NoError(t, dataformat.InitializeCatalog(db))
	require.NoError(t, applyConfigurationPlan(ctx, db, filename, plan))
	var locations []*library.Location
	require.NoError(t, db.Find(&locations).Error)
	require.Len(t, locations, 1)
	previewSettings, err := configurationSettings(db).Preview.Current(ctx)
	require.NoError(t, err)
	require.True(t, proto.Equal(plan.Preview, previewSettings))

	// The same conversion is now a no-op, with the reviewed YAML and permissions preserved.
	next, err := planConfiguration(ctx, db, filename)
	require.NoError(t, err)
	require.False(t, next.Changed)
	actual, err := os.ReadFile(filename)
	require.NoError(t, err)
	require.Equal(t, plan.YAML, string(actual))
	info, err := os.Stat(filename)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

func TestConfigurationPlanConvertsLegacyPathsAndPreviewWithoutWriting(t *testing.T) {
	ctx, root := context.Background(), t.TempDir()
	t.Chdir(root)
	require.NoError(t, os.MkdirAll("source", 0o755))
	require.NoError(t, os.MkdirAll("restore", 0o755))
	db, err := resource.OpenSQLite(filepath.Join(root, "catalog.db"))
	require.NoError(t, err)
	require.NoError(t, library.New(db).AutoMigrate())
	filename := filepath.Join(root, "config.yaml")
	original := "# keep this comment\nunknown: retained\npaths:\n  source: ./source\n  target: ./restore\npreview:\n  generators:\n    - kind: image\n      extensions: [jpg]\n      options:\n        max_width: 240\n        max_height: 200\n        quality: 60\n"
	require.NoError(t, os.WriteFile(filename, []byte(original), 0o640))

	plan, err := planConfiguration(ctx, db, filename)
	require.NoError(t, err)
	require.True(t, plan.Changed)
	require.True(t, plan.ImportLocations)
	require.NotNil(t, plan.Preview)
	require.EqualValues(t, 240, plan.Preview.Generators[0].GetImage().MaxWidth)
	require.Contains(t, plan.YAML, "# keep this comment")
	require.Contains(t, plan.YAML, "unknown: retained")
	require.NotContains(t, plan.YAML, "source:")
	require.NotContains(t, plan.YAML, "target:")
	require.NotContains(t, plan.YAML, "generators:")
	require.Contains(t, plan.YAML, "access:")
	require.Contains(t, plan.YAML, filepath.Join(root, "source"))
	require.Contains(t, plan.YAML, filepath.Join(root, "restore"))
	unchanged, err := os.ReadFile(filename)
	require.NoError(t, err)
	require.Equal(t, original, string(unchanged), "planning is read-only")
	info, err := os.Stat(filename)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())

	require.NoError(t, checkConfigurationPlan(ctx, db, filename, plan))
	require.NoError(t, applyConfigurationPlan(ctx, db, filename, plan))
	updated, err := os.ReadFile(filename)
	require.NoError(t, err)
	require.Equal(t, plan.YAML, string(updated))
	info, err = os.Stat(filename)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())

	next, err := planConfiguration(ctx, db, filename)
	require.NoError(t, err)
	require.False(t, next.Changed)
	previewSettings, err := configurationSettings(db).Preview.Current(ctx)
	require.NoError(t, err)
	require.True(t, proto.Equal(plan.Preview, previewSettings))
}

func TestConfigurationPlanRejectsStaleConfigurationAndSettings(t *testing.T) {
	ctx, root := context.Background(), t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "source"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "restore"), 0o755))
	db, err := resource.OpenSQLite(filepath.Join(root, "catalog.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	filename := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(filename, []byte("paths:\n  source: "+filepath.Join(root, "source")+"\n  target: "+filepath.Join(root, "restore")+"\n"), 0o600))
	plan, err := planConfiguration(ctx, db, filename)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filename, []byte("unknown: changed\n"), 0o600))
	require.Error(t, checkConfigurationPlan(ctx, db, filename, plan))

	require.NoError(t, os.WriteFile(filename, []byte("paths:\n  source: "+filepath.Join(root, "source")+"\n  target: "+filepath.Join(root, "restore")+"\n"), 0o600))
	plan, err = planConfiguration(ctx, db, filename)
	require.NoError(t, err)
	settings, err := configurationSettings(db).Library.Current(ctx)
	require.NoError(t, err)
	settings.IncludeUnbackedFiles = false
	_, err = configurationSettings(db).Library.Save(ctx, settings)
	require.NoError(t, err)
	require.Error(t, checkConfigurationPlan(ctx, db, filename, plan))

	// A separately stored Job group participates in the same reviewed state.
	plan, err = planConfiguration(ctx, db, filename)
	require.NoError(t, err)
	job, err := configurationSettings(db).Job.Current(ctx)
	require.NoError(t, err)
	job.Execution.ReadBatch++
	_, err = configurationSettings(db).Job.Save(ctx, job)
	require.NoError(t, err)
	require.Error(t, checkConfigurationPlan(ctx, db, filename, plan))
}

func TestConfigurationPlanRejectsAmbiguousLegacyYAML(t *testing.T) {
	ctx := context.Background()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "catalog.db"))
	require.NoError(t, err)
	require.NoError(t, library.New(db).AutoMigrate())
	filename := filepath.Join(t.TempDir(), "config.yaml")
	require.NoError(t, os.WriteFile(filename, []byte("paths:\n  source: one\n  source: two\n"), 0o600))
	_, err = planConfiguration(ctx, db, filename)
	require.Error(t, err)
}

func TestConfigurationPlanPreservesExplicitAccessAndSavedPreferences(t *testing.T) {
	ctx, root := context.Background(), t.TempDir()
	source, target := filepath.Join(root, "source"), filepath.Join(root, "restore")
	require.NoError(t, os.Mkdir(source, 0o755))
	require.NoError(t, os.Mkdir(target, 0o755))
	db, err := resource.OpenSQLite(filepath.Join(root, "catalog.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	preview, err := configurationSettings(db).Preview.Current(ctx)
	require.NoError(t, err)
	preview.Generators = []*entity.PreviewGeneratorSettings{{Enabled: true, Extensions: []*entity.PreviewExtension{{Name: "jpg", Enabled: true}}, Options: &entity.PreviewGeneratorSettings_Image{Image: &entity.ImagePreviewSettings{MaxWidth: 99, MaxHeight: 99, Format: "png", Quality: 80}}}}
	_, err = configurationSettings(db).Preview.Save(ctx, preview)
	require.NoError(t, err)
	filename := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(filename, []byte("paths:\n  source: "+source+"\n  target: "+target+"\n  access: []\npreview:\n  generators: []\n"), 0o600))

	plan, err := planConfiguration(ctx, db, filename)
	require.NoError(t, err)
	require.True(t, plan.ImportLocations, "explicit offline conversion imports the remaining legacy paths")
	require.Nil(t, plan.Preview)
	require.Contains(t, plan.YAML, "access: []")
	require.Contains(t, plan.Settings, "Preserve saved Preview preferences.")
	require.NoError(t, applyConfigurationPlan(ctx, db, filename, plan))
	require.False(t, db.Migrator().HasTable("location_migrations"))
	var locations []library.Location
	require.NoError(t, db.Find(&locations).Error)
	require.Len(t, locations, 2)

	// Removing converted YAML keys is the completion marker; later edits do not recreate deleted roots.
	require.NoError(t, db.Delete(&locations[0]).Error)
	next, err := planConfiguration(ctx, db, filename)
	require.NoError(t, err)
	require.False(t, next.ImportLocations)
	require.False(t, next.Changed)
}

func TestConfigurationPlanTreatsEqualLegacyRootsAsOneLocation(t *testing.T) {
	ctx, root := context.Background(), t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	shared := filepath.Join(root, "shared")
	require.NoError(t, os.Mkdir(shared, 0o755))
	db, err := resource.OpenSQLite(filepath.Join(root, "catalog.db"))
	require.NoError(t, err)
	require.NoError(t, library.New(db).AutoMigrate())
	filename := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(filename, []byte("paths:\n  source: "+shared+"\n  target: "+shared+"\n"), 0o600))
	plan, err := planConfiguration(ctx, db, filename)
	require.NoError(t, err)
	require.True(t, plan.ImportLocations)
	require.Len(t, plan.Settings, 2, "one Location import plus Preview settings")
	require.Contains(t, plan.Settings[0], "preferred restore target: true")
	require.NoError(t, applyConfigurationPlan(ctx, db, filename, plan))
	var locations []*library.Location
	require.NoError(t, db.Order("id").Find(&locations).Error)
	require.Len(t, locations, 1)
	require.Equal(t, shared, locations[0].RootPath)
	require.True(t, locations[0].RestoreTarget)
}

func TestConfigurationPlanPreservesRegisteredRootAndAccessMeaning(t *testing.T) {
	ctx, root := context.Background(), t.TempDir()
	var err error
	root, err = filepath.EvalSymlinks(root)
	require.NoError(t, err)
	source, target := filepath.Join(root, "source"), filepath.Join(root, "restore")
	require.NoError(t, os.Mkdir(source, 0o755))
	require.NoError(t, os.Mkdir(target, 0o755))
	db, err := resource.OpenSQLite(filepath.Join(root, "catalog.db"))
	require.NoError(t, err)
	lib := library.New(db)
	require.NoError(t, lib.AutoMigrate())
	registered := &library.Location{Name: "Kept name", ExecutorID: "local", RootPath: source, RestoreTarget: false,
		Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "private/\n"}}}
	require.NoError(t, lib.CreateLocation(ctx, registered))
	filename := filepath.Join(root, "config.yaml")
	require.NoError(t, os.WriteFile(filename, []byte("paths:\n  source: "+source+"\n  target: "+target+"\n  access:\n    - root: /already-explicit\n"), 0o600))
	plan, err := planConfiguration(ctx, db, filename)
	require.NoError(t, err)
	require.Contains(t, plan.YAML, "root: /already-explicit")
	require.NoError(t, applyConfigurationPlan(ctx, db, filename, plan))
	kept, err := lib.GetLocation(ctx, registered.ID)
	require.NoError(t, err)
	require.Equal(t, "Kept name", kept.Name)
	require.False(t, kept.RestoreTarget)
	require.Equal(t, "private/\n", kept.Config.GetIgnore().GetText())

	// Null retains the legacy derived-access meaning and is made explicit from both old roots.
	nullFile := filepath.Join(root, "null-access.yaml")
	require.NoError(t, os.WriteFile(nullFile, []byte("paths:\n  source: "+source+"\n  target: "+target+"\n  access: null\n"), 0o600))
	nullPlan, err := planConfiguration(ctx, db, nullFile)
	require.NoError(t, err)
	require.Contains(t, nullPlan.YAML, "root: "+source)
	require.Contains(t, nullPlan.YAML, "root: "+target)
}
