//go:build linux || darwin

package apis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"github.com/samuelncui/yatm/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func selectionStageDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := resource.OpenSQLite(filepath.Join(t.TempDir(), "job.db"))
	require.NoError(t, err)
	require.NoError(t, executor.InitSelectionObservations(db))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func physicalSelection(locationID int64, path string) *entity.FileSelection {
	return &entity.FileSelection{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: locationID, Path: path}}}
}

func TestLiveSelectionStagesNativeBeforeCopiedUUIDAcrossPages(t *testing.T) {
	// Archive and Preview share this preparation path, regardless of traversal or page ordering.
	api, service, dir, id := admissionLocation(t)
	ctx := context.Background()
	original := filepath.Join(dir, "original.txt")
	require.NoError(t, os.WriteFile(original, []byte("original"), 0644))
	trackingID := uuid.NewString()
	trackingAttribute(t, original, trackingID)
	first := admittedPage(t, service, id)["original.txt"]
	note := "continuous managed object"
	require.NoError(t, api.lib.EditFileMetadata(ctx, []int64{first.File.Id}, library.FileMetadataEdit{Note: &note}))
	locationBefore, err := api.lib.GetOnlineSource(ctx, id)
	require.NoError(t, err)

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
	require.Equal(t, first.File.Id, renamed.FileID)
	copy, err := api.lib.GetFileLocationAtPath(ctx, id, "a-copy.txt")
	require.NoError(t, err)
	require.NotEqual(t, first.File.Id, copy.FileID)
	file, err := api.lib.GetFile(ctx, renamed.FileID)
	require.NoError(t, err)
	require.Equal(t, note, file.Note)
	locationAfter, err := api.lib.GetOnlineSource(ctx, id)
	require.NoError(t, err)
	require.Equal(t, locationBefore.LastSyncAt, locationAfter.LastSyncAt)
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
	require.Equal(t, first.File.Id, current.FileID)
	moved, err := api.lib.GetFileLocationAtPath(ctx, id, "a-moved.txt")
	require.NoError(t, err)
	require.NotEqual(t, first.File.Id, moved.FileID)
}

func TestLiveSelectionDirectoryDriftDoesNotPublishAssociations(t *testing.T) {
	api, service, dir, id := admissionLocation(t)
	ctx := context.Background()
	original := filepath.Join(dir, "original.txt")
	require.NoError(t, os.WriteFile(original, []byte("managed object"), 0644))
	first := admittedPage(t, service, id)["original.txt"]
	require.NoError(t, os.Rename(original, filepath.Join(dir, "renamed.txt")))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("not admitted yet"), 0644))
	db := selectionStageDB(t)
	changed := false
	require.NoError(t, db.Callback().Update().After("gorm:update").Register("test:change_directory_after_matching", func(tx *gorm.DB) {
		if changed || tx.Statement.Table != "observations" {
			return
		}
		changed = true
		require.NoError(t, os.WriteFile(filepath.Join(dir, "arrived-later.txt"), []byte("directory drift"), 0644))
	}))
	err := api.exe.WalkLiveSelections(ctx, db, []*entity.FileSelection{physicalSelection(id, "")}, func(*library.File, string) error {
		t.Fatal("changed scope must not reach the content manifest")
		return nil
	})
	require.True(t, changed)
	require.Error(t, err)
	old, err := api.lib.GetFileLocation(ctx, first.File.Id)
	require.NoError(t, err)
	require.Equal(t, "original.txt", old.Path)
	unadmitted, err := api.lib.GetFileLocationAtPath(ctx, id, "new.txt")
	require.NoError(t, err)
	require.Nil(t, unadmitted)
}

func TestLiveSelectionCachedHashPreservesOpaqueSignature(t *testing.T) {
	api, service, dir, id := admissionLocation(t)
	ctx := context.Background()
	filename := filepath.Join(dir, "opaque.txt")
	require.NoError(t, os.WriteFile(filename, []byte("known immutable content"), 0644))
	reader, err := acp.New(ctx, acp.AccurateJob(filename, nil), acp.WithHash(true), acp.WithSignatureCache(true))
	require.NoError(t, err)
	require.NoError(t, reader.WaitErr())
	first := admittedPage(t, service, id)["opaque.txt"]
	location, err := api.lib.GetOnlineSource(ctx, id)
	require.NoError(t, err)
	opaque := []byte{0, 255, 7, 1, 9}
	_, err = api.lib.AdmitObservation(ctx, id, location.BindingToken, &library.OnlinePosition{FileID: first.File.Id, SourceID: id, Path: "opaque.txt",
		Size: first.Original.Size, Mode: first.Original.Mode, MtimeNS: first.Original.MtimeNs, Signature: opaque, Hash: first.Original.Sha256})
	require.NoError(t, err)
	for _, name := range []string{"opaque.txt", "moved.txt"} {
		if name != "opaque.txt" {
			require.NoError(t, os.Rename(filename, filepath.Join(dir, name)))
		}
		require.NoError(t, api.exe.WalkLiveSelections(ctx, selectionStageDB(t), []*entity.FileSelection{physicalSelection(id, name)}, func(*library.File, string) error { return nil }))
		original, err := api.lib.GetFileLocationAtPath(ctx, id, name)
		require.NoError(t, err)
		require.Equal(t, first.File.Id, original.FileID)
		require.Equal(t, opaque, original.Signature)
	}
}

func TestLiveSelectionKnownCopyDoesNotInheritRemovedOriginal(t *testing.T) {
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
	location, err := api.lib.GetOnlineSource(ctx, id)
	require.NoError(t, err)
	info, err := os.Lstat(target)
	require.NoError(t, err)
	receipt := &library.FileOperationResult{OperationID: uuid.NewString(), ItemID: 1, LocationID: id, BindingToken: location.BindingToken,
		Kind: entity.FileOperationKind_COPY, SourcePath: "original.txt", TargetPath: "copy.txt", OutputIdentity: executor.LocationFacts(info).Identity}
	require.NoError(t, api.lib.PublishFileOperation(ctx, receipt))
	require.NoError(t, os.Remove(before))
	require.NoError(t, os.Link(target, filepath.Join(dir, "hardlink.txt")))
	require.NoError(t, api.exe.WalkLiveSelections(ctx, selectionStageDB(t), []*entity.FileSelection{physicalSelection(id, "")}, func(*library.File, string) error { return nil }))
	copy, err := api.lib.GetFileLocationAtPath(ctx, id, "copy.txt")
	require.NoError(t, err)
	require.NotEqual(t, first.File.Id, copy.FileID)
	hardlink, err := api.lib.GetFileLocationAtPath(ctx, id, "hardlink.txt")
	require.NoError(t, err)
	require.NotEqual(t, first.File.Id, hardlink.FileID)
	require.NotEqual(t, copy.FileID, hardlink.FileID)

	// A consumed receipt can follow its own File, but cannot merge another live hardlink's organization.
	require.NoError(t, os.Rename(target, filepath.Join(dir, "moved-copy.txt")))
	require.NoError(t, api.exe.WalkLiveSelections(ctx, selectionStageDB(t), []*entity.FileSelection{physicalSelection(id, "")}, func(*library.File, string) error { return nil }))
	moved, err := api.lib.GetFileLocationAtPath(ctx, id, "moved-copy.txt")
	require.NoError(t, err)
	require.Equal(t, copy.FileID, moved.FileID)
	kept, err := api.lib.GetFileLocationAtPath(ctx, id, "hardlink.txt")
	require.NoError(t, err)
	require.Equal(t, hardlink.FileID, kept.FileID)
}

func TestLiveSelectionFreezesIdentityBeforeConcurrentPathReplacement(t *testing.T) {
	// Another ordinary Location operation can run between selected-file callbacks after admission releases its gate.
	api, service, dir, id := admissionLocation(t)
	ctx := context.Background()
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0644))
	}
	initial := admittedPage(t, service, id)
	location, err := api.lib.GetOnlineSource(ctx, id)
	require.NoError(t, err)
	var selectedIDs []int64
	var selectedPaths []string
	selections := []*entity.FileSelection{physicalSelection(id, "a.txt"), physicalSelection(id, "b.txt")}
	err = api.exe.WalkLiveSelections(ctx, selectionStageDB(t), selections, func(file *library.File, _ string) error {
		if len(selectedIDs) == 0 {
			// Move the selected B safely, then occupy its old path with a different managed File C.
			release, err := api.lib.UseOnlineSource(id)
			require.NoError(t, err)
			for index, move := range [][2]string{{"b.txt", "moved-b.txt"}, {"c.txt", "b.txt"}} {
				require.NoError(t, os.Rename(filepath.Join(dir, move[0]), filepath.Join(dir, move[1])))
				require.NoError(t, api.lib.PublishFileOperation(ctx, &library.FileOperationResult{OperationID: uuid.NewString(), ItemID: int64(index + 1),
					LocationID: id, BindingToken: location.BindingToken, Kind: entity.FileOperationKind_MOVE, SourcePath: move[0], TargetPath: move[1]}))
			}
			release()
		}
		filename, expected, err := api.exe.CaptureOriginal(ctx, file.ID)
		if err != nil {
			return err
		}
		selectedIDs = append(selectedIDs, expected.FileId)
		selectedPaths = append(selectedPaths, filepath.Base(filename))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []int64{initial["a.txt"].File.Id, initial["b.txt"].File.Id}, selectedIDs)
	require.Equal(t, []string{"a.txt", "moved-b.txt"}, selectedPaths)
	replacement, err := api.lib.GetFileLocationAtPath(ctx, id, "b.txt")
	require.NoError(t, err)
	require.Equal(t, initial["c.txt"].File.Id, replacement.FileID)
}

func TestLiveSelectionChecksFactsAfterContentPreparation(t *testing.T) {
	// A callback may have staged bytes, but changed input must prevent the executable manifest checkpoint.
	api, _, dir, id := admissionLocation(t)
	ctx := context.Background()
	filename := filepath.Join(dir, "fresh.txt")
	require.NoError(t, os.WriteFile(filename, []byte("selected bytes"), 0644))
	err := api.exe.WalkLiveSelections(ctx, selectionStageDB(t), []*entity.FileSelection{physicalSelection(id, "fresh.txt")}, func(file *library.File, _ string) error {
		require.Positive(t, file.ID, "new admission must freeze its allocated ID before releasing the gate")
		_, _, err := api.exe.CaptureOriginal(ctx, file.ID)
		if err != nil {
			return err
		}
		return os.WriteFile(filename, []byte("changed after content preparation"), 0644)
	})
	require.ErrorIs(t, err, library.ErrOnlineConflict)
}
