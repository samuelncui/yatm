package library

import (
	"context"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestFileRowRejectsUnsupportedIdentity(t *testing.T) {
	// Direct row writes and batched imports share the persisted identity boundary.
	for _, name := range []string{"", ".", "..", "a/b", "nul\x00", "invalid\xff"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			// A failed insert or batch must leave the existing valid File intact.
			db, _ := newTestLibrary(t)
			kept := &fileRow{Name: "kept", Kind: entity.FileKind_FILE_KIND_REGULAR}
			require.NoError(t, db.Create(kept).Error)
			require.Error(t, db.Create(&fileRow{Name: name, Kind: kept.Kind}).Error)
			batch := []fileRow{{Name: "batch-valid", Kind: kept.Kind}, {Name: name, Kind: kept.Kind}}
			require.Error(t, db.Create(&batch).Error)

			// Full-row saves and partial name updates cannot bypass component validation.
			changed := *kept
			changed.Name = name
			require.Error(t, db.Save(&changed).Error)
			for _, column := range []string{"name", "Name"} {
				require.Error(t, db.Model(ModelFile).Where("id = ?", kept.ID).Update(column, name).Error)
			}
			require.Error(t, db.Model(kept).Select("name").Updates(fileRow{Name: name}).Error)
			require.Error(t, db.Model(kept).Select("name").Updates(&fileRow{Name: name}).Error)
			var rows []fileRow
			require.NoError(t, db.Find(&rows).Error)
			require.Len(t, rows, 1)
			require.Equal(t, "kept", rows[0].Name)
		})
	}
}

func TestFileRowPreservesLiteralUTF8AndPartialUpdates(t *testing.T) {
	// Ordinary row writes preserve legal text including a genuine replacement character.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	for _, name := range []string{`back\slash`, " leading", "trailing ", " \t\n", "quote'\"\n照片", "�"} {
		row := &fileRow{Name: name, Kind: entity.FileKind_FILE_KIND_REGULAR}
		require.NoError(t, db.Create(row).Error)
		row.Note = "saved"
		require.NoError(t, db.Save(row).Error)
		require.NoError(t, db.Model(ModelFile).Where("id = ?", row.ID).Update("note", "edited").Error)
		require.NoError(t, db.Model(ModelFile).Where("id = ?", row.ID).Update("parent_id", 0).Error)
		require.NoError(t, db.Model(row).Updates(&fileRow{Note: "struct edit"}).Error)
		require.NoError(t, db.Model(row).Select("note").Updates(map[string]any{"name": "ignored\xff", "note": "edited"}).Error)
		stored, err := lib.GetFile(ctx, row.ID)
		require.NoError(t, err)
		require.Equal(t, name, stored.Name)
		require.Equal(t, "edited", stored.Note)
	}

	// Root remains virtual, while Trash retains its normal persisted directory identity.
	root, err := lib.GetByPath(ctx, 0, "/")
	require.NoError(t, err)
	current, err := lib.MkdirAll(ctx, 0, ".", 0o755)
	require.NoError(t, err)
	require.Equal(t, root.ID, current.ID)
	checkpoint, err := lib.newTrash(ctx, db)
	require.NoError(t, err)
	require.EqualValues(t, TrashFileID, checkpoint.ParentID)
	trash, err := lib.GetFile(ctx, TrashFileID)
	require.NoError(t, err)
	require.Equal(t, ".Trash", trash.Name)
	var count int64
	require.NoError(t, db.Model(ModelFile).Where("id = ?", 0).Count(&count).Error)
	require.Zero(t, count)
}
