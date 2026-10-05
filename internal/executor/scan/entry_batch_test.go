package scan

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

func TestEntryPersistenceBoundsSQLIndependentlyOfResultBatch(t *testing.T) {
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "entries.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer func() { require.NoError(t, sqlDB.Close()) }()
	require.NoError(t, db.AutoMigrate(&Entry{}))
	r := &runner{db: db}

	// A valid large ACP batch exceeds SQLite's parameter limit as one INSERT.
	rows := make([]*Entry, 2000)
	for i := range rows {
		rows[i] = &Entry{ID: int64(i + 1), Path: fmt.Sprintf("file-%04d", i), Size: int64(i)}
	}
	require.NoError(t, r.saveEntries(context.Background(), rows))
	for _, row := range rows {
		row.Size++
	}
	require.NoError(t, r.saveEntries(context.Background(), rows))
	var totals struct{ Count, Size int64 }
	require.NoError(t, db.Model(&Entry{}).Select("COUNT(*) AS count, SUM(size) AS size").Scan(&totals).Error)
	require.EqualValues(t, len(rows), totals.Count)
	require.EqualValues(t, len(rows)*(len(rows)+1)/2, totals.Size)
}
