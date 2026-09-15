package archive

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
)

func TestArchiveSchemaCreatesCurrentTables(t *testing.T) {
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "state.db"))
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&executor.JobRecord{}))
	require.NoError(t, ensureSchema(context.Background(), db))
	require.True(t, db.Migrator().HasColumn(&Item{}, "target_path"))
	require.True(t, db.Migrator().HasColumn(&Item{}, "media_path"))
	require.True(t, db.Migrator().HasColumn(&Item{}, "media_id"))
	var tables []string
	require.NoError(t, db.Raw(
		"SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name",
	).Scan(&tables).Error)
	require.Equal(t, []string{"config", "items", "job", "observations", "originals", "raw_inputs", "selection_directories"}, tables)
}
