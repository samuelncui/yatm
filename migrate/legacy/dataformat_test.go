package legacy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/internal/dataformat"
	legacypb "github.com/samuelncui/yatm/migrate/legacy/pb"
	"github.com/stretchr/testify/require"
)

func TestCommitRejectsUnsupportedPreparedBundle(t *testing.T) {
	// A successful prepare may not authorize a later replacement with Draft or future state.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	root := t.TempDir()
	require.NoError(t, db.Create(&legacyJob{ID: 1, Status: legacypb.JobStatus_COMPLETED,
		State: &legacypb.JobState{State: &legacypb.JobState_Archive{Archive: &legacypb.JobArchiveState{}}},
	}).Error)
	report, err := Prepare(ctx, db, root)
	require.NoError(t, err)
	require.True(t, report.Success)
	filename := filepath.Join(root, "jobs", "1", "job.json")
	draft := []byte(`{"format_version":2,"id":1}`)
	require.NoError(t, os.WriteFile(filename, draft, 0o644))

	// Reject before any table activation, keeping the original legacy schema and staged evidence.
	require.ErrorContains(t, Commit(ctx, db, root), "unsupported Job bundle format")
	schema, err := DetectSchema(db)
	require.NoError(t, err)
	require.Equal(t, SchemaLegacy, schema)
	require.False(t, db.Migrator().HasTable(dataformat.CatalogTable))
	require.False(t, db.Migrator().HasTable("jobs_legacy"))
	require.True(t, db.Migrator().HasTable("jobs_staging"))
	actual, err := os.ReadFile(filename)
	require.NoError(t, err)
	require.Equal(t, draft, actual)
}

func TestOfflineOperationsRejectFutureCatalogWithoutWrites(t *testing.T) {
	// Marker presence is authoritative even when the database also resembles genuine legacy.
	ctx := context.Background()
	db := newLegacyTestDB(t)
	require.NoError(t, dataformat.MarkCatalog(db))
	require.NoError(t, db.Table(dataformat.CatalogTable).Where("id = 1").Update("revision", 2).Error)
	root := t.TempDir()
	reportPath := filepath.Join(root, reportFilename)
	evidence := []byte("retained report")
	require.NoError(t, os.WriteFile(reportPath, evidence, 0o644))
	_, err := Prepare(ctx, db, root)
	require.ErrorIs(t, err, dataformat.ErrUnsupportedCatalog)
	require.ErrorIs(t, Commit(ctx, db, root), dataformat.ErrUnsupportedCatalog)
	require.ErrorIs(t, Abort(db, root), dataformat.ErrUnsupportedCatalog)
	require.ErrorIs(t, Cleanup(db, root), dataformat.ErrUnsupportedCatalog)
	require.ErrorIs(t, RepairJob(ctx, db, root, 1), dataformat.ErrUnsupportedCatalog)

	// Neither cleanup nor the prepare error-report path may replace preexisting evidence.
	actual, err := os.ReadFile(reportPath)
	require.NoError(t, err)
	require.Equal(t, evidence, actual)
	require.False(t, db.Migrator().HasTable("jobs_staging"))
	var revision int
	require.NoError(t, db.Table(dataformat.CatalogTable).Select("revision").Scan(&revision).Error)
	require.Equal(t, 2, revision)
}
