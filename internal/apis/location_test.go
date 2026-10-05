package apis

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/preview"
	"github.com/samuelncui/yatm/internal/resource"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
)

func setupLocationAPI(t *testing.T) (*API, *locationService, string) {
	// Use an isolated permitted namespace with explicit runtime resources below it.
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	db, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	settings := settingspkg.New(db, settingspkg.PreviewDefinition{
		Default:  func() (*entity.PreviewSettings, error) { return preview.SettingsFromConfig(preview.Config{}) },
		Validate: preview.ValidateSettings,
	})
	l := library.NewWithSettings(db, settings)
	require.NoError(t, l.AutoMigrate())
	exe := executor.New(db, l, nil, executor.Paths{Access: []executor.AccessRange{{Root: root}}, Work: filepath.Join(root, "work")}, executor.Scripts{}, nil)
	api := New(l, exe)
	return api, &locationService{api: api}, root
}

func TestLocationRegistrationBoundariesAndConfiguration(t *testing.T) {
	// Authored rules are preserved; an unconfigured registration starts from the default rules.
	_, service, root := setupLocationAPI(t)
	ctx := context.Background()
	created, err := service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Whole root", RootPath: root, Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "future/"}, UseMmap: true}}})
	require.NoError(t, err)
	require.Equal(t, "future/", created.Location.Config.GetIgnore().GetText())
	require.True(t, created.Location.Config.GetUseMmap())
	defaultedRoot := filepath.Join(root, "defaulted")
	require.NoError(t, os.Mkdir(defaultedRoot, 0755))
	defaulted, err := service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Default rules", RootPath: defaultedRoot}})
	require.NoError(t, err)
	require.Contains(t, defaulted.Location.Config.GetIgnore().GetText(), ".yatm.json")
	require.False(t, defaulted.Location.Config.GetUseMmap())
	changed, err := service.Update(ctx, &entity.UpdateLocationRequest{Location: &entity.Location{
		Id: created.Location.Id, Name: "Whole root", RootPath: root,
		Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "future/"}},
	}})
	require.NoError(t, err)
	require.False(t, changed.Location.Config.GetUseMmap())
	reloaded, err := service.Get(ctx, &entity.GetLocationRequest{Id: created.Location.Id})
	require.NoError(t, err)
	require.False(t, reloaded.Location.Config.GetUseMmap())
	_, err = service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Duplicate", RootPath: root}})
	require.Error(t, err)

	// Known symlinks and lexical path escapes are refused at the registration boundary.
	out := t.TempDir()
	require.NoError(t, os.Symlink(out, filepath.Join(root, "linked")))
	for _, invalid := range []string{out, filepath.Join(root, "linked")} {
		_, err := service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Invalid", RootPath: invalid}})
		require.Error(t, err)
	}

	// Nested roots remain distinct registrations with an explicit duplicate-index warning.
	sub := filepath.Join(root, "documents")
	require.NoError(t, os.Mkdir(sub, 0755))
	nested, err := service.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Documents", RootPath: sub}})
	require.NoError(t, err)
	require.NotEmpty(t, nested.Warnings)
}
