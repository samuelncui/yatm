package library

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/dataformat"
	"github.com/samuelncui/yatm/internal/resource"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCatalogReadersAndTransactionViewsUseTheirOwnedConnections(t *testing.T) {
	// Exercise Library and Settings consumers, not only raw SQLite connections.
	catalog, err := resource.OpenCatalog("sqlite", filepath.Join(t.TempDir(), "catalog.db"), true, func(db *gorm.DB) error {
		_, err := dataformat.CheckCatalog(db)
		return err
	})
	require.NoError(t, err)
	defer func() { require.NoError(t, catalog.Close()) }()
	settings := settingspkg.NewWithCatalog(catalog.Write, catalog.Read, settingspkg.PreviewDefinition{})
	lib := NewWithCatalog(catalog.Write, catalog.Read, settings)
	require.NoError(t, lib.AutoMigrate())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	location := &Location{Name: "original", ExecutorID: "local", RootPath: t.TempDir(), Config: &entity.LocationConfig{}}
	require.NoError(t, lib.CreateLocation(ctx, location))
	_, err = settings.Library.Save(ctx, &entity.LibrarySettings{IncludeUnbackedFiles: true})
	require.NoError(t, err)

	// The writer sees its own updates while public readers still see committed values.
	tx := catalog.Write.WithContext(ctx).Begin()
	require.NoError(t, tx.Error)
	defer func() { _ = tx.Rollback().Error }()
	require.NoError(t, tx.Model(&Location{}).Where("id = ?", location.ID).Update("name", "changed").Error)
	view := &Library{db: tx, settings: settings.WithDB(tx)}
	_, err = view.settings.Library.Save(ctx, &entity.LibrarySettings{ConfirmRemove: true})
	require.NoError(t, err)
	committed, err := lib.GetLocation(ctx, location.ID)
	require.NoError(t, err)
	require.Equal(t, "original", committed.Name)
	current, err := view.GetLocation(ctx, location.ID)
	require.NoError(t, err)
	require.Equal(t, "changed", current.Name)
	committedSettings, err := settings.Library.Current(ctx)
	require.NoError(t, err)
	require.True(t, committedSettings.IncludeUnbackedFiles)
	currentSettings, err := view.settings.Library.Current(ctx)
	require.NoError(t, err)
	require.False(t, currentSettings.IncludeUnbackedFiles)
	require.True(t, currentSettings.ConfirmRemove)

	// The same long-lived readers see the next committed transaction normally.
	require.NoError(t, tx.Commit().Error)
	committed, err = lib.GetLocation(ctx, location.ID)
	require.NoError(t, err)
	require.Equal(t, "changed", committed.Name)
	committedSettings, err = settings.Library.Current(ctx)
	require.NoError(t, err)
	require.True(t, committedSettings.ConfirmRemove)
}
