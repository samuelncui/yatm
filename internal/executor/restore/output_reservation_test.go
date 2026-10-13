package restore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestReserveOutputUsesFrozenPathIndexAndPreservesCollisions(t *testing.T) {
	// Start from a normal prepared Restore, then retain one frozen path among unchosen rows.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	_, file := createMediaFile(t, lib, newTapeMedia("RESERVE"), ".", "saved.txt", []byte("saved"), nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var frozen File
	require.NoError(t, runner.db.First(&frozen).Error)
	require.NoError(t, runner.db.Model(&File{}).Where("item_id = ?", frozen.ItemID).Update("path", "collision.txt").Error)
	pending := make([]File, 64)
	for index := range pending {
		pending[index] = File{ItemID: frozen.ItemID + int64(index) + 1}
	}
	require.NoError(t, runner.db.Create(&pending).Error)
	names, err := newOutputNames(ctx, runner)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, names.close()) })

	// Capture the executed GORM queries so the actual Go SQLite driver owns the plan assertion.
	var queries []struct {
		sql  string
		args []any
	}
	const callback = "test:restore-reservation-plan"
	require.NoError(t, runner.db.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "files" && strings.Contains(strings.ToLower(tx.Statement.SQL.String()), "count(*)") {
			queries = append(queries, struct {
				sql  string
				args []any
			}{tx.Statement.SQL.String(), append([]any(nil), tx.Statement.Vars...)})
		}
	}))
	t.Cleanup(func() { require.NoError(t, runner.db.Callback().Query().Remove(callback)) })

	// Empty pending paths do not collide; a frozen nonempty path still chooses the existing suffix policy.
	digest := sha256.Sum256([]byte("saved"))
	for index, desired := range []string{"available.txt", "collision.txt"} {
		version := &library.FileVersion{ID: pending[index].ItemID, FileID: file.ID, Hash: digest[:], Size: 5}
		want := desired
		if index == 1 {
			want = library.RestoredName(desired, version.ID)
		}
		target, satisfied, err := runner.reserveOutput(ctx, names, version, desired)
		require.NoError(t, err)
		require.Equal(t, want, target)
		require.False(t, satisfied)
		require.NoFileExists(t, filepath.Join(exe.Paths().Target, target))
		again, _, err := runner.reserveOutput(ctx, names, version, desired)
		require.NoError(t, err)
		require.Equal(t, target, again, "a frozen reservation must not acquire another suffix")
	}
	require.Len(t, queries, 3)

	// Candidate lookups use the existing covering partial index, independently of timing noise.
	for _, query := range queries {
		var plan []struct{ Detail string }
		require.NoError(t, runner.db.Raw("EXPLAIN QUERY PLAN "+query.sql, query.args...).Scan(&plan).Error)
		require.Contains(t, fmt.Sprint(plan), "COVERING INDEX idx_restore_file_path")
		require.NotContains(t, fmt.Sprint(plan), "SCAN files")
	}
	require.Error(t, entity.ValidateRelativePath(""), "manifest admission rejects an empty desired path")
}
