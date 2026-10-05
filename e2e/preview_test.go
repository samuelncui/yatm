//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/apis"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	previewcore "github.com/samuelncui/yatm/internal/preview"
	"github.com/samuelncui/yatm/internal/resource"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestPreviewGenerationPolicy(t *testing.T) {
	requirePreviewHelper(t)
	// Keep the source stable while testing different derivative policies.
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	sourcePath := filepath.Join(sourceRoot, "image.png")
	require.NoError(t, os.MkdirAll(sourceRoot, 0o755))
	image, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(sourcePath, image, 0o644))

	// Create the first content-addressed bundle with the initial generator settings.
	initial, signature := runPreviewPolicyJob(t, root, sourceRoot, "initial", 240, 80, false)
	manifest, err := initial.Manifest(signature)
	require.NoError(t, err)
	require.JSONEq(t, `{"max_height":240,"max_width":240,"format":"png","quality":80}`, string(manifest.SettingsJson))

	// Missing-only keeps an existing bundle even if generator settings change.
	updated, sameSignature := runPreviewPolicyJob(t, root, sourceRoot, "updated-disabled", 120, 60, false)
	require.Equal(t, signature, sameSignature)
	manifest, err = updated.Manifest(signature)
	require.NoError(t, err)
	require.JSONEq(t, `{"max_height":240,"max_width":240,"format":"png","quality":80}`, string(manifest.SettingsJson))

	// Regenerate-all replaces an existing bundle using the Job's frozen preferences.
	updated, sameSignature = runPreviewPolicyJob(t, root, sourceRoot, "updated-enabled", 120, 60, true)
	require.Equal(t, signature, sameSignature)
	manifest, err = updated.Manifest(signature)
	require.NoError(t, err)
	require.JSONEq(t, `{"max_height":120,"max_width":120,"format":"png","quality":60}`, string(manifest.SettingsJson))
}

func runPreviewPolicyJob(
	t *testing.T,
	root string,
	sourceRoot string,
	name string,
	width int32,
	quality int32,
	force bool,
) (*previewcore.Manager, []byte) {
	t.Helper()

	// Start one isolated Executor while sharing only the durable Preview bundle root.
	databaseRoot := filepath.Join(root, "runs", name)
	require.NoError(t, os.MkdirAll(databaseRoot, 0o755))
	executorDB, err := resource.OpenSQLite(filepath.Join(databaseRoot, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(databaseRoot, "library.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		closeDatabase(t, executorDB)
		closeDatabase(t, libraryDB)
	})
	appSettings := settingspkg.New(libraryDB, settingspkg.PreviewDefinition{
		Default:  func() (*entity.PreviewSettings, error) { return previewcore.SettingsFromConfig(previewcore.Config{}) },
		Validate: previewcore.ValidateSettings,
	})
	lib := library.NewWithSettings(libraryDB, appSettings)
	require.NoError(t, lib.AutoMigrate())

	// Exercise deployment-relative storage through the real Scan and helper boundary.
	workingDir, err := os.Getwd()
	require.NoError(t, err)
	previewRoot, err := filepath.Rel(workingDir, filepath.Join(root, "previews"))
	require.NoError(t, err)
	require.False(t, filepath.IsAbs(previewRoot))
	previews, err := previewcore.NewWithSettings(context.Background(), previewRoot, "", appSettings.Preview.Current)
	require.NoError(t, err)
	exe := executor.New(executorDB, lib, nil, executor.Paths{
		Work: filepath.Join(databaseRoot, "work"), Source: sourceRoot,
		Access: []executor.AccessRange{{Root: sourceRoot}},
	}, executor.Scripts{}, previews)
	require.NoError(t, exe.AutoMigrate())

	// Register and generate from unadmitted real files through CLI, without an Analyze prerequisite.
	ctx := context.Background()
	connection := serveCLI(t, apis.New(lib, exe), exe)
	settings, err := previewcore.SettingsFromConfig(previewcore.Config{Generators: []previewcore.GeneratorConfig{{
		Kind: "image", Extensions: []string{"png"}, Options: map[string]any{"max_width": width, "max_height": width, "format": "png", "quality": quality},
	}}})
	require.NoError(t, err)
	settings.Enabled = true
	settings.Command = requirePreviewHelper(t)
	encoded, err := protojson.Marshal(settings)
	require.NoError(t, err)
	settingsPath := filepath.Join(databaseRoot, "preview-settings.json")
	require.NoError(t, os.WriteFile(settingsPath, encoded, 0o600))
	cliResult(t, ctx, connection, new(entity.PreviewSettings), "settings", "preview", "--preview-json", settingsPath)
	selections := liveSelections(t, ctx, connection, sourceRoot, "image.png")
	capabilities := new(entity.GetPreviewCapabilitiesResponse)
	cliResult(t, ctx, connection, capabilities, "preview", "capabilities")
	require.True(t, capabilities.Available, capabilities.Reason)
	require.Contains(t, capabilities.Kinds, "image")
	job := new(entity.CreateScanJobResponse)
	args := []string{"preview", "create", "--priority", "1", "--location", decimal(selections[0].GetLocation().LocationId) + ":image.png"}
	if force {
		args = append(args, "--preview-policy", "regenerate-all")
	}
	cliResult(t, ctx, connection, job, args...)
	_, err = connection.run(ctx, "job", "wait", decimal(job.Job.Id), "--wait-timeout", "30s", "--poll-interval", "100ms")
	require.NoError(t, err)

	// Read the generated bundle by the same content identity observed by the Scan.
	cached, valid, err := acp.ReadCachedSignature(filepath.Join(sourceRoot, "image.png"))
	require.NoError(t, err)
	require.True(t, valid)
	signature, err := library.NewFileSignature(cached.SHA256[:], cached.Size)
	require.NoError(t, err)
	return previews, signature
}

func requirePreviewHelper(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("YATM_TEST_PREVIEW_HELPER"); path != "" {
		resolved, err := filepath.Abs(path)
		require.NoError(t, err)
		return resolved
	}
	path, err := exec.LookPath("yatm-preview")
	if err != nil {
		t.Skip("set YATM_TEST_PREVIEW_HELPER for native Preview acceptance")
	}
	return path
}

func closeDatabase(t *testing.T, db interface{ DB() (*sql.DB, error) }) {
	t.Helper()
	sqlDB, err := db.DB()
	if err == nil {
		require.NoError(t, sqlDB.Close())
	}
}
