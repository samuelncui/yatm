package library

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestApplyScanUpdatesPhysicalPositionsAtomically(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	modified := time.Unix(10, 0)
	oldChanged := sha256.Sum256([]byte("old changed"))
	removed := sha256.Sum256([]byte("removed"))
	stable := sha256.Sum256([]byte("stable"))

	// Publish the existing Volume checkpoint and materialize its logical Files.
	media, err := lib.CommitMedia(ctx, &Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: "11111111-1111-1111-1111-111111111111", Name: "Volume",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack(),
	}, func(_ context.Context, yield func(*MediaFile) error) error {
		for _, file := range []*MediaFile{
			{Path: "changed.txt", Size: 11, Mode: 0o644, ModTime: modified, Hash: oldChanged[:]},
			{Path: "removed.txt", Size: 7, Mode: 0o644, ModTime: modified, Hash: removed[:]},
			{Path: "stable.txt", Size: 6, Mode: 0o644, ModTime: modified, Hash: stable[:]},
		} {
			if err := yield(file); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.NoError(t, lib.ImportArchivedInventory(ctx))
	oldFile, err := lib.GetByPath(ctx, Root.ID, "Unforged/Volume/changed.txt")
	require.NoError(t, err)

	// Apply one added, changed, and removed path while leaving the absent stable path untouched.
	added := sha256.Sum256([]byte("added"))
	changed := sha256.Sum256([]byte("new changed"))
	applied, err := lib.ApplyScan(ctx, media.ID, func(_ context.Context, yield func(*entity.ScanEntry) error) error {
		for _, entry := range []*entity.ScanEntry{
			{Path: "added.txt", Change: entity.ScanChange_SCAN_CHANGE_ADDED, Size: 5, Mode: uint32(fs.FileMode(0o644)), MtimeNs: modified.UnixNano(), Sha256: added[:]},
			{Path: "changed.txt", Change: entity.ScanChange_SCAN_CHANGE_CHANGED, Size: 11, Mode: uint32(fs.FileMode(0o600)), MtimeNs: modified.Add(time.Second).UnixNano(), Sha256: changed[:]},
			{Path: "removed.txt", Change: entity.ScanChange_SCAN_CHANGE_REMOVED},
		} {
			if err := yield(entry); err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, int64(22), applied.WrittenBytes)

	// Scan changes inventory only; saved File history and organization remain unchanged.
	var positions []*Position
	require.NoError(t, db.Where("media_id = ? AND is_dir = ?", media.ID, false).Order("path").Find(&positions).Error)
	require.Equal(t, []string{"added.txt", "changed.txt", "stable.txt"}, []string{
		positions[0].Path, positions[1].Path, positions[2].Path,
	})
	require.Equal(t, changed[:], positions[1].Hash)
	require.Empty(t, positions[1].StorageOrder)
	newFile, err := lib.GetByPath(ctx, Root.ID, "Unforged/Volume/changed.txt")
	require.NoError(t, err)
	require.Equal(t, oldFile.ID, newFile.ID)
	require.Equal(t, oldFile.Hash, newFile.Hash)
	preserved, err := lib.GetFile(ctx, oldFile.ID)
	require.NoError(t, err)
	parents, err := lib.ListParents(ctx, preserved.ID)
	require.NoError(t, err)
	require.NotEqual(t, int64(TrashFileID), parents[0].ID)
	copies, err := lib.GetPositionByFileID(ctx, oldFile.ID)
	require.NoError(t, err)
	require.Empty(t, copies)
}

func TestApplyScanRollsBackInvalidDiff(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	hash := sha256.Sum256([]byte("fixture"))
	media, err := lib.CommitMedia(ctx, &Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: "22222222-2222-2222-2222-222222222222",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack(),
	}, func(_ context.Context, yield func(*MediaFile) error) error {
		return yield(&MediaFile{Path: "file.txt", Size: 7, Mode: 0o644, Hash: hash[:]})
	})
	require.NoError(t, err)

	// Reject an invalid later entry and roll back the earlier added Position and directory rebuild.
	_, err = lib.ApplyScan(ctx, media.ID, func(_ context.Context, yield func(*entity.ScanEntry) error) error {
		if err := yield(&entity.ScanEntry{
			Path: "added.txt", Change: entity.ScanChange_SCAN_CHANGE_ADDED,
			Size: 7, Mode: 0o644, Sha256: hash[:],
		}); err != nil {
			return err
		}
		return yield(&entity.ScanEntry{
			Path: "../invalid", Change: entity.ScanChange_SCAN_CHANGE_ADDED,
			Size: 7, Mode: 0o644, Sha256: hash[:],
		})
	})
	require.Error(t, err)
	var paths []string
	require.NoError(t, db.Model(ModelPosition).Where("media_id = ? AND is_dir = ?", media.ID, false).
		Order("path").Pluck("path", &paths).Error)
	require.Equal(t, []string{"file.txt"}, paths)
}
