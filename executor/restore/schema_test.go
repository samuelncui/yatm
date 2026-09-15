package restore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

func TestRestoreSchemaCreatesOnlyPlannedTables(t *testing.T) {
	// Create the complete Restore bundle schema, including durable output reservations.
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&executor.JobRecord{}))
	require.NoError(t, ensureSchema(context.Background(), db))

	// Execution tables stay kind-specific and do not leak into the shared catalog.
	var tables []string
	require.NoError(t, db.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name",
	).Scan(&tables).Error)
	require.Equal(t, []string{"config", "copies", "files", "job", "outputs"}, tables)
}
