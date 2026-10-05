package restore

import (
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestRestoreSchemaCreatesOnlyPlannedTables(t *testing.T) {
	// Create the complete Restore bundle schema, including durable output reservations.
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&executor.JobRecord{}))
	require.NoError(t, db.AutoMigrate(&Config{}, &Copy{}, &File{}))

	// Execution tables stay kind-specific and do not leak into the shared catalog.
	var tables []string
	require.NoError(t, db.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name",
	).Scan(&tables).Error)
	require.Equal(t, []string{"config", "copies", "files", "job"}, tables)

	// The physical candidate row contains no selected File projection fields.
	var columns []string
	require.NoError(t, db.Raw("SELECT name FROM pragma_table_info('copies') ORDER BY cid").Scan(&columns).Error)
	require.Equal(t, []string{
		"id", "item_id", "status", "media_id", "media_path", "storage_order", "media_identity", "media_profile",
		"position_id", "health", "health_checked_at_ns", "health_published",
	}, columns)
	require.True(t, db.Migrator().HasIndex(&Copy{}, "idx_copies_media_status_order"))
}
