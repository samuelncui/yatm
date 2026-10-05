//go:build linux || darwin

package apis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func selectionStageDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "job.db"))
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func physicalSelection(locationID int64, path string) *entity.FileSelection {
	return &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: locationID, Path: path}}}
}

func TestLiveSelectionUsesNativeIdentityAcrossPages(t *testing.T) {
	// Archive and Preview share this preparation path, regardless of traversal or page ordering.
	api, service, dir, id := admissionLocation(t)
	ctx := context.Background()
	original := filepath.Join(dir, "original.txt")
	require.NoError(t, os.WriteFile(original, []byte("original"), 0644))
	trackingID := uuid.NewString()
	trackingAttribute(t, original, trackingID)
	first := admittedPage(t, service, id)["original.txt"]
	note := "continuous managed object"
	require.NoError(t, api.lib.EditFileMetadata(ctx, []int64{first.Entry.GetAssociatedFileId()}, library.FileMetadataEdit{Note: &note}))
	locationBefore, err := api.lib.GetLocation(ctx, id)
	require.NoError(t, err)

	// Resolve an early copy and late rename across batches without publishing a Scan timestamp.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a-copy.txt"), []byte("independent edited copy"), 0644))
	trackingAttribute(t, filepath.Join(dir, "a-copy.txt"), trackingID)
	for index := 0; index < 270; index++ {
		require.NoError(t, os.WriteFile(filepath.Join(dir, fmt.Sprintf("middle-%03d.txt", index)), []byte("unadmitted"), 0644))
	}
	require.NoError(t, os.Rename(original, filepath.Join(dir, "z-renamed.txt")))
	var files int
	require.NoError(t, api.exe.WalkLiveSelections(ctx, selectionStageDB(t), []*entity.FileSelection{physicalSelection(id, "")}, func(*library.File, string) error {
		files++
		return nil
	}))
	require.Equal(t, 272, files)
	renamed, err := api.lib.GetFileLocationAtPath(ctx, id, "z-renamed.txt")
	require.NoError(t, err)
	require.Equal(t, first.Entry.GetAssociatedFileId(), renamed.FileID)
	copy, err := api.lib.GetFileLocationAtPath(ctx, id, "a-copy.txt")
	require.NoError(t, err)
	require.NotEqual(t, first.Entry.GetAssociatedFileId(), copy.FileID)
	file, err := api.lib.GetFile(ctx, renamed.FileID)
	require.NoError(t, err)
	require.Equal(t, note, file.Note)
	locationAfter, err := api.lib.GetLocation(ctx, id)
	require.NoError(t, err)
	require.Equal(t, locationBefore.LastSyncAtNS, locationAfter.LastSyncAtNS)
	require.Equal(t, locationBefore.LastSyncJobID, locationAfter.LastSyncJobID)
}

func TestLiveSelectionAllPathsPrecedeOtherSelectedRoots(t *testing.T) {
	api, service, dir, id := admissionLocation(t)
	ctx := context.Background()
	original := filepath.Join(dir, "original.txt")
	require.NoError(t, os.WriteFile(original, []byte("old object"), 0644))
	first := admittedPage(t, service, id)["original.txt"]
	require.NoError(t, os.Rename(original, filepath.Join(dir, "a-moved.txt")))
	require.NoError(t, os.WriteFile(original, []byte("replacement stays with managed path"), 0644))
	selections := []*entity.FileSelection{physicalSelection(id, "a-moved.txt"), physicalSelection(id, "original.txt"), physicalSelection(id, "")}
	require.NoError(t, api.exe.WalkLiveSelections(ctx, selectionStageDB(t), selections, func(*library.File, string) error { return nil }))
	current, err := api.lib.GetFileLocationAtPath(ctx, id, "original.txt")
	require.NoError(t, err)
	require.Equal(t, first.Entry.GetAssociatedFileId(), current.FileID)
	moved, err := api.lib.GetFileLocationAtPath(ctx, id, "a-moved.txt")
	require.NoError(t, err)
	require.NotEqual(t, first.Entry.GetAssociatedFileId(), moved.FileID)
}

func TestLiveSelectionCachedHashPreservesOpaqueSignature(t *testing.T) {
	api, service, dir, id := admissionLocation(t)
	ctx := context.Background()
	filename := filepath.Join(dir, "opaque.txt")
	require.NoError(t, os.WriteFile(filename, []byte("known immutable content"), 0644))
	cacheContentSignature(t, ctx, filename)
	first := admittedPage(t, service, id)["opaque.txt"]
	recorded, err := api.lib.GetFileLocation(ctx, first.Entry.GetAssociatedFileId())
	require.NoError(t, err)
	opaque := []byte{0, 255, 7, 1, 9}
	_, err = api.lib.AdmitObservation(ctx, id, &library.ObservedEntry{FileID: recorded.FileID, Path: "opaque.txt",
		Size: recorded.Size, Mode: recorded.Mode, MtimeNS: recorded.MtimeNS, Signature: opaque, Hash: recorded.Hash})
	require.NoError(t, err)
	for _, name := range []string{"opaque.txt", "moved.txt"} {
		if name != "opaque.txt" {
			require.NoError(t, os.Rename(filename, filepath.Join(dir, name)))
		}
		require.NoError(t, api.exe.WalkLiveSelections(ctx, selectionStageDB(t), []*entity.FileSelection{physicalSelection(id, name)}, func(*library.File, string) error { return nil }))
		original, err := api.lib.GetFileLocationAtPath(ctx, id, name)
		require.NoError(t, err)
		require.Equal(t, first.Entry.GetAssociatedFileId(), original.FileID)
		require.Equal(t, opaque, original.Signature)
	}
}

func TestLiveSelectionCopyDoesNotInheritExistingOriginal(t *testing.T) {
	api, service, dir, id := admissionLocation(t)
	ctx := context.Background()
	before := filepath.Join(dir, "original.txt")
	require.NoError(t, os.WriteFile(before, []byte("copied bytes"), 0644))
	trackingID := uuid.NewString()
	trackingAttribute(t, before, trackingID)
	first := admittedPage(t, service, id)["original.txt"]
	target := filepath.Join(dir, "copy.txt")
	require.NoError(t, os.WriteFile(target, []byte("copied bytes"), 0644))
	trackingAttribute(t, target, trackingID)
	require.NoError(t, os.Link(target, filepath.Join(dir, "hardlink.txt")))
	require.NoError(t, api.exe.WalkLiveSelections(ctx, selectionStageDB(t), []*entity.FileSelection{physicalSelection(id, "")}, func(*library.File, string) error { return nil }))
	copy, err := api.lib.GetFileLocationAtPath(ctx, id, "copy.txt")
	require.NoError(t, err)
	require.NotEqual(t, first.Entry.GetAssociatedFileId(), copy.FileID)
	hardlink, err := api.lib.GetFileLocationAtPath(ctx, id, "hardlink.txt")
	require.NoError(t, err)
	require.NotEqual(t, first.Entry.GetAssociatedFileId(), hardlink.FileID)
	require.NotEqual(t, copy.FileID, hardlink.FileID)

	// A tracked copy follows its own File without merging another live hardlink.
	require.NoError(t, os.Rename(target, filepath.Join(dir, "moved-copy.txt")))
	require.NoError(t, api.exe.WalkLiveSelections(ctx, selectionStageDB(t), []*entity.FileSelection{physicalSelection(id, "")}, func(*library.File, string) error { return nil }))
	moved, err := api.lib.GetFileLocationAtPath(ctx, id, "moved-copy.txt")
	require.NoError(t, err)
	require.Equal(t, copy.FileID, moved.FileID)
	kept, err := api.lib.GetFileLocationAtPath(ctx, id, "hardlink.txt")
	require.NoError(t, err)
	require.Equal(t, hardlink.FileID, kept.FileID)
}

func TestLiveSelectionObeysLocationIgnore(t *testing.T) {
	// Ignore scopes this Location's content, so explicitly selected files and folders are not staged.
	api, _, dir, id := admissionLocation(t)
	ctx := context.Background()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "ignored"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored", "data.bin"), []byte("excluded"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kept.txt"), []byte("kept"), 0644))
	location, err := api.lib.GetLocation(ctx, id)
	require.NoError(t, err)
	location.Config.Ignore = &entity.IgnoreRules{Format: "gitignore", Text: "ignored/\n"}
	_, err = api.lib.UpdateLocation(ctx, location)
	require.NoError(t, err)

	var names []string
	require.NoError(t, api.exe.WalkLiveSelections(ctx, selectionStageDB(t), []*entity.FileSelection{
		physicalSelection(id, "ignored/data.bin"), physicalSelection(id, "ignored"), physicalSelection(id, "kept.txt"),
	}, func(file *library.File, _ string) error {
		names = append(names, file.Name)
		return nil
	}))
	require.Equal(t, []string{"kept.txt"}, names)
}
