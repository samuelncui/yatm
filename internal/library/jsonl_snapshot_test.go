package library

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type concurrentExportWriter struct {
	bytes.Buffer
	update func() error
}

func (w *concurrentExportWriter) Write(data []byte) (int, error) {
	// The first entity read establishes the snapshot before a concurrent writer commits.
	if w.update != nil && bytes.HasPrefix(data, []byte(`{"type":"file",`)) {
		update := w.update
		w.update = nil
		if err := update(); err != nil {
			return 0, err
		}
	}
	return w.Buffer.Write(data)
}

func TestLibraryWALExportRestoresOneSnapshotAcrossPages(t *testing.T) {
	// Keep the actual read pool separate from the sole writer and cross an export page boundary.
	catalog, err := resource.OpenCatalog("sqlite", filepath.Join(t.TempDir(), "catalog.db"), true, func(db *gorm.DB) error {
		return New(db).AutoMigrate()
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, catalog.Close()) })
	source := NewWithCatalog(catalog.Write, catalog.Read, nil)
	files := make([]*File, batchSize+1)
	for i := range files {
		files[i] = &File{ID: int64(i + 1), Name: fmt.Sprintf("original-%d", i+1)}
	}
	createFileRows(t, source.db, files...)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot := &concurrentExportWriter{update: func() error {
		return source.db.WithContext(ctx).Model(ModelFile).Where("id IN ?", []int64{1, int64(batchSize + 1)}).Update("note", "changed").Error
	}}
	require.NoError(t, source.Export(ctx, snapshot, []entity.LibraryEntityType{entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_FILE}))
	require.Nil(t, snapshot.update, "the writer must commit while the export is open")

	// Import sees only the pre-write state, including the row in the next page.
	target := newJSONLTestLibrary(t)
	require.NoError(t, target.Import(ctx, bytes.NewReader(snapshot.Bytes()), false))
	for _, id := range []int64{1, int64(batchSize + 1)} {
		original, err := target.GetFile(ctx, id)
		require.NoError(t, err)
		require.Equal(t, fmt.Sprintf("original-%d", id), original.Name)
		require.Empty(t, original.Note)
		current, err := source.GetFile(ctx, id)
		require.NoError(t, err)
		require.Equal(t, "changed", current.Note)
	}
}
