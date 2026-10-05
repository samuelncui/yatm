package library

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFileReadsUseCatalogReaderAndTransactionsKeepTheirOwnView(t *testing.T) {
	// Real read-only WAL connections must remain usable while the sole writer owns an uncommitted change.
	catalog, err := resource.OpenCatalog("sqlite", filepath.Join(t.TempDir(), "catalog.db"), true, func(db *gorm.DB) error {
		return New(db).AutoMigrate()
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, catalog.Close()) })
	lib := NewWithCatalog(catalog.Write, catalog.Read, nil)
	file := &File{Name: "committed"}
	require.NoError(t, lib.SaveFile(context.Background(), file))
	tx := catalog.Write.Begin()
	require.NoError(t, tx.Error)
	defer tx.Rollback()
	require.NoError(t, tx.Model(&fileRow{}).Where("id = ?", file.ID).Update("name", "pending").Error)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stored, err := lib.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, "committed", stored.Name)
	node, err := lib.FileTree().Stat(ctx, strconv.FormatInt(file.ID, 10))
	require.NoError(t, err)
	require.Equal(t, "committed", node.Name)
	_, err = lib.MGetFileTags(ctx, file.ID)
	require.NoError(t, err)
	_, err = lib.MergeFileMembers(ctx, file.ID, func(func([]int64) error) error { return nil }, true)
	require.NoError(t, err)

	// A transaction-local Library reads its own write, and subsequent logical mutations use the writer.
	local := &Library{db: tx}
	stored, err = local.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, "pending", stored.Name)
	require.NoError(t, tx.Rollback().Error)
	require.NoError(t, lib.Delete(ctx, []int64{file.ID}))
	stored, err = lib.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.NotZero(t, stored.ParentID)
}
