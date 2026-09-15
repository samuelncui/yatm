//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/apis"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	previewcore "github.com/samuelncui/yatm/preview"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

func TestPreviewGenerationPolicy(t *testing.T) {
	// Keep the source stable while testing different derivative policies.
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source")
	sourcePath := filepath.Join(sourceRoot, "image.fixture")
	require.NoError(t, os.MkdirAll(sourceRoot, 0o755))
	require.NoError(t, os.WriteFile(sourcePath, []byte("stable Preview content"), 0o644))

	// Create the first content-addressed bundle with the initial generator settings.
	initial, signature := runPreviewPolicyJob(t, root, sourceRoot, "initial", false)
	manifest, err := initial.Manifest(signature)
	require.NoError(t, err)
	require.JSONEq(t, `{"marker":"initial"}`, string(manifest.SettingsJson))

	// Missing-only keeps an existing bundle even if generator settings change.
	updated, sameSignature := runPreviewPolicyJob(t, root, sourceRoot, "updated-disabled", false)
	require.Equal(t, signature, sameSignature)
	manifest, err = updated.Manifest(signature)
	require.NoError(t, err)
	require.JSONEq(t, `{"marker":"initial"}`, string(manifest.SettingsJson))

	// Regenerate-all replaces an existing bundle using the configured generator.
	updated, sameSignature = runPreviewPolicyJob(t, root, sourceRoot, "updated-enabled", true)
	require.Equal(t, signature, sameSignature)
	manifest, err = updated.Manifest(signature)
	require.NoError(t, err)
	require.JSONEq(t, `{"marker":"updated-enabled"}`, string(manifest.SettingsJson))
}

func runPreviewPolicyJob(
	t *testing.T,
	root string,
	sourceRoot string,
	marker string,
	force bool,
) (*previewcore.Manager, []byte) {
	t.Helper()

	// Start one isolated Executor while sharing only the durable Preview bundle root.
	databaseRoot := filepath.Join(root, "runs", marker)
	require.NoError(t, os.MkdirAll(databaseRoot, 0o755))
	executorDB, err := resource.OpenSQLite(filepath.Join(databaseRoot, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(databaseRoot, "library.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		closeDatabase(t, executorDB)
		closeDatabase(t, libraryDB)
	})
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	previews, err := previewcore.New(previewcore.Config{
		Root: filepath.Join(root, "previews"),
		Generators: []previewcore.GeneratorConfig{{
			Kind: "e2e-fixture", Extensions: []string{"fixture"}, Options: map[string]any{"marker": marker},
		}},
	}, "")
	require.NoError(t, err)
	exe := executor.New(executorDB, lib, nil, executor.Paths{
		Work: filepath.Join(databaseRoot, "work"), Source: sourceRoot,
	}, executor.Scripts{}, previews)
	require.NoError(t, exe.AutoMigrate())

	// Register and generate from unadmitted real files through CLI, without an Analyze prerequisite.
	ctx := context.Background()
	connection := serveCLI(t, apis.New(lib, exe), exe)
	selections := liveSelections(t, ctx, connection, sourceRoot, "image.fixture")
	policy := entity.PreviewPolicy_PREVIEW_MISSING_ONLY
	if force {
		policy = entity.PreviewPolicy_PREVIEW_REGENERATE_ALL
	}
	job, err := entity.NewScanJobServiceClient(connection).Create(ctx, &entity.CreateScanJobRequest{Priority: 1, Spec: &entity.ScanJobSpec{
		Selections: selections, PreviewPolicy: policy,
	}})
	require.NoError(t, err)
	_, err = connection.run(ctx, "job", "wait", decimal(job.Job.Id), "--wait-timeout", "30s", "--poll-interval", "100ms")
	require.NoError(t, err)

	cached, valid, err := acp.ReadCachedSignature(filepath.Join(sourceRoot, "image.fixture"))
	require.NoError(t, err)
	require.True(t, valid)
	signature, err := library.NewFileSignature(cached.SHA256[:], cached.Size)
	require.NoError(t, err)
	return previews, signature
}

func closeDatabase(t *testing.T, db interface{ DB() (*sql.DB, error) }) {
	t.Helper()
	sqlDB, err := db.DB()
	if err == nil {
		require.NoError(t, sqlDB.Close())
	}
}
