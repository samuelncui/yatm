package fileops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRemoveRecyclesWholeDirectoryAndDisconnectsDescendants(t *testing.T) {
	// Include ignored files, empty directories and a symlink without touching its target.
	f := setup(t)
	ctx := context.Background()
	f.write(t, "folder/a.txt", "a")
	f.write(t, "folder/nested/ignored.txt", "b")
	f.write(t, "outside.txt", "outside")
	require.NoError(t, os.Mkdir(filepath.Join(f.root, "folder/empty"), 0755))
	require.NoError(t, os.Symlink("../../outside.txt", filepath.Join(f.root, "folder/nested/link")))
	first, second := f.original(t, "folder/a.txt"), f.original(t, "folder/nested/ignored.txt")
	version := &library.FileVersion{FileID: first.ID, Signature: []byte("saved")}
	require.NoError(t, f.libDB.Create(version).Error)
	f.location.Config.Ignore = &entity.IgnoreRules{Format: "gitignore", Text: "ignored.txt\n"}
	require.NoError(t, f.libDB.Save(f.location).Error)

	// Remove emits one successful physical scope, preserving all bytes inside Trash.
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE,
		Sources: []*entity.FileOperationRef{f.ref(t, "folder"), f.ref(t, "folder/a.txt")}}, nil)
	require.EqualValues(t, 1, stream.summary().SucceededCount)
	target := stream.updates[1].Entry.TargetPath
	require.True(t, executor.IsLocationTrashContent(target))
	require.NoDirExists(t, filepath.Join(f.root, "folder"))
	require.FileExists(t, filepath.Join(f.root, target, "a.txt"))
	require.FileExists(t, filepath.Join(f.root, target, "nested/ignored.txt"))
	require.DirExists(t, filepath.Join(f.root, target, "empty"))
	link, err := os.Readlink(filepath.Join(f.root, target, "nested/link"))
	require.NoError(t, err)
	require.Equal(t, "../../outside.txt", link)
	require.FileExists(t, filepath.Join(f.root, "outside.txt"))
	for _, file := range []*library.File{first, second} {
		original, err := f.exe.Lib().GetFileLocation(ctx, file.ID)
		require.NoError(t, err)
		require.Nil(t, original)
		stored, err := f.exe.Lib().GetFile(ctx, file.ID)
		require.NoError(t, err)
		require.Equal(t, file.Name, stored.Name)
		require.Equal(t, file.Note, stored.Note)
	}
	require.NoError(t, f.libDB.First(new(library.FileVersion), version.ID).Error)

	// Browsing and Move out work, but neither physical removal nor fresh admission is implicit.
	trashRef := f.ref(t, target)
	require.ErrorContains(t, (&service{exe: f.exe}).execute(&entity.FileOperationSpec{
		Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE, Sources: []*entity.FileOperationRef{trashRef}}, false, &operationStream{ctx: ctx}), "only be moved out")
	_, err = f.exe.AdmitLocationEntries(ctx, []*entity.LocationEntryRef{f.ref(t, target+"/a.txt").GetLocation()})
	require.ErrorContains(t, err, "Trash")
	f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_MOVE,
		Sources: []*entity.FileOperationRef{trashRef}, Destination: f.ref(t, ""), Name: "returned"}, nil)
	require.FileExists(t, filepath.Join(f.root, "returned/a.txt"))
	original, err := f.exe.Lib().GetFileLocation(ctx, first.ID)
	require.NoError(t, err)
	require.Nil(t, original)
}

func TestRemoveSameNamesUsesIndependentContainers(t *testing.T) {
	// Independent selections with identical basenames cannot merge or overwrite one another.
	f := setup(t)
	f.write(t, "first/same.txt", "first")
	f.write(t, "second/same.txt", "second")
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE,
		Sources: []*entity.FileOperationRef{f.ref(t, "first/same.txt"), f.ref(t, "second/same.txt")}}, nil)
	require.EqualValues(t, 2, stream.summary().SucceededCount)
	first, second := stream.updates[1].Entry.TargetPath, stream.updates[2].Entry.TargetPath
	require.NotEqual(t, first, second)
	for name, expected := range map[string]string{first: "first", second: "second"} {
		data, err := os.ReadFile(filepath.Join(f.root, name))
		require.NoError(t, err)
		require.Equal(t, expected, string(data))
	}
}

func TestConcurrentFirstRemoveInitializesTrashWithoutSerializingPhysicalMoves(t *testing.T) {
	// Keep originals and saved history for independent removals and an untouched neighbor.
	f := setup(t)
	ctx := context.Background()
	f.write(t, "first.txt", "first")
	f.write(t, "second.txt", "second")
	f.write(t, "untouched.txt", "untouched")
	files := []*library.File{f.original(t, "first.txt"), f.original(t, "second.txt"), f.original(t, "untouched.txt")}
	for _, file := range files {
		require.NoError(t, f.libDB.Create(&library.FileTrackingKey{FileID: file.ID, LocationID: f.location.ID,
			Kind: library.TrackingNative, KeyValue: []byte(file.Name)}).Error)
		require.NoError(t, f.libDB.Create(&library.FileVersion{FileID: file.ID, Signature: []byte(file.Name)}).Error)
	}

	// Start two requests against the same unused Trash after both have prepared distinct files.
	refs := []*entity.FileOperationRef{f.ref(t, "first.txt"), f.ref(t, "second.txt")}
	ready, start := make(chan struct{}, 2), make(chan struct{})
	entered, second, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var blocked atomic.Bool
	type result struct {
		stream *operationStream
		err    error
	}
	done := make(chan result, len(refs))
	for _, ref := range refs {
		go func() {
			stream := &operationStream{ctx: context.Background(), onSend: func(update *entity.FileOperationResult) error {
				if update.GetEntry() == nil && !update.GetSummary().GetCompleted() {
					ready <- struct{}{}
					<-start
				} else if update.GetEntry() != nil {
					if blocked.CompareAndSwap(false, true) {
						close(entered)
						<-release
					} else {
						close(second)
					}
				}
				return nil
			}}
			err := Remove(f.exe, &entity.RemoveFilesRequest{Sources: []*entity.FileOperationRef{ref}}, stream)
			done <- result{stream: stream, err: err}
		}()
	}
	for range refs {
		select {
		case <-ready:
		case <-time.After(10 * time.Second):
			close(start)
			close(release)
			t.Fatal("concurrent Removes did not both finish preparation")
		}
	}
	close(start)

	// The second physical move must finish while the first request is held after publication.
	var physicalConcurrent bool
	select {
	case <-entered:
		select {
		case <-second:
			physicalConcurrent = true
		case <-time.After(10 * time.Second):
		}
	case <-time.After(10 * time.Second):
	}
	close(release)
	targets := make(map[string]string, len(refs))
	for range refs {
		select {
		case result := <-done:
			require.NoError(t, result.err)
			require.EqualValues(t, 1, result.stream.summary().SucceededCount)
			entry := result.stream.updates[1].GetEntry()
			targets[entry.GetSourcePath()] = entry.GetTargetPath()
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent Remove did not finish")
		}
	}
	require.True(t, physicalConcurrent, "one physical Remove must finish while the other request remains active")

	// Both payloads occupy separate containers beneath one valid ownership marker.
	require.NoError(t, validateTrash(f.root))
	entries, err := os.ReadDir(filepath.Join(f.root, executor.LocationTrashDirectory))
	require.NoError(t, err)
	require.Len(t, entries, 3)
	require.NotEqual(t, filepath.Dir(targets["first.txt"]), filepath.Dir(targets["second.txt"]))
	for name, content := range map[string]string{"first.txt": "first", "second.txt": "second"} {
		require.NoFileExists(t, filepath.Join(f.root, name))
		require.True(t, executor.IsLocationTrashContent(targets[name]))
		data, err := os.ReadFile(filepath.Join(f.root, targets[name]))
		require.NoError(t, err)
		require.Equal(t, content, string(data))
	}

	// Each publication detaches only its selected original and retains independent history.
	for index, file := range files {
		original, err := f.exe.Lib().GetFileLocation(ctx, file.ID)
		require.NoError(t, err)
		tracking, err := f.exe.Lib().ReadFileTracking(ctx, file.ID)
		require.NoError(t, err)
		if index < 2 {
			require.Nil(t, original)
			require.Empty(t, tracking)
		} else {
			require.NotNil(t, original)
			require.Equal(t, "untouched.txt", original.Path)
			require.Len(t, tracking, 1)
		}
		stored, err := f.exe.Lib().GetFile(ctx, file.ID)
		require.NoError(t, err)
		require.Equal(t, file.Name, stored.Name)
		require.Equal(t, file.Note, stored.Note)
		versions, _, err := f.exe.Lib().ListFileVersions(ctx, file.ID, 0, 10)
		require.NoError(t, err)
		require.Len(t, versions, 1)
	}
	require.FileExists(t, filepath.Join(f.root, "untouched.txt"))
}

func TestRemoveRejectsUnownedTrash(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "directory", true: "symlink"}[symlink], func(t *testing.T) {
			// Existing user paths are not taken over even when their names match the reserved root.
			f := setup(t)
			f.write(t, "keep.txt", "keep")
			if symlink {
				require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(f.root, ".trash")))
			} else {
				f.write(t, ".trash/sentinel", "untouched")
			}
			stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE,
				Sources: []*entity.FileOperationRef{f.ref(t, "keep.txt")}}, nil)
			require.EqualValues(t, 1, stream.summary().FailedCount)
			require.FileExists(t, filepath.Join(f.root, "keep.txt"))
			if !symlink {
				require.FileExists(t, filepath.Join(f.root, ".trash/sentinel"))
			}
		})
	}

	// A same-sized foreign marker cannot claim ownership or be replaced.
	t.Run("foreign marker", func(t *testing.T) {
		f := setup(t)
		f.write(t, "keep.txt", "keep")
		marker := "X" + trashIdentity[1:]
		f.write(t, filepath.Join(executor.LocationTrashDirectory, executor.LocationTrashMarker), marker)
		stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE,
			Sources: []*entity.FileOperationRef{f.ref(t, "keep.txt")}}, nil)
		require.EqualValues(t, 1, stream.summary().FailedCount)
		require.FileExists(t, filepath.Join(f.root, "keep.txt"))
		data, err := os.ReadFile(filepath.Join(f.root, executor.LocationTrashDirectory, executor.LocationTrashMarker))
		require.NoError(t, err)
		require.Equal(t, marker, string(data))
	})
}

func TestTrashPublicationFailureRetainsOutputAndOriginalMetadata(t *testing.T) {
	// A metadata transaction may fail after the guarded move, without losing the actual output.
	f := setup(t)
	f.write(t, "source/file.txt", "retained")
	file := f.original(t, "source/file.txt")
	require.NoError(t, f.libDB.Callback().Update().Before("gorm:update").Register("reject-trash-publication", func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*library.Location); ok {
			tx.AddError(errors.New("injected publication failure"))
		}
	}))
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE,
		Sources: []*entity.FileOperationRef{f.ref(t, "source")}}, nil)
	require.EqualValues(t, 1, stream.summary().PublicationPendingCount)
	target := stream.updates[1].Entry.TargetPath
	require.True(t, executor.IsLocationTrashContent(target))
	require.FileExists(t, filepath.Join(f.root, target, "file.txt"))
	original, err := f.exe.Lib().GetFileLocation(context.Background(), file.ID)
	require.NoError(t, err)
	require.NotNil(t, original, "failed metadata transaction keeps prior state for explicit reconciliation")
}

func TestTrashCannotBeRestoreDestinationOrManualMoveTarget(t *testing.T) {
	// Reservation is a business boundary, not a global ban on browsing recycled data.
	f := setup(t)
	f.write(t, "removed.txt", "removed")
	stream := f.execute(t, &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE,
		Sources: []*entity.FileOperationRef{f.ref(t, "removed.txt")}}, nil)
	target := stream.updates[1].Entry.TargetPath
	f.write(t, "keep.txt", "keep")
	_, err := f.exe.FreezeRestoreDestination(context.Background(), &entity.RestoreDestination{
		LocationId: f.location.ID, Path: filepath.ToSlash(filepath.Dir(target))})
	require.ErrorContains(t, err, "Trash")
	err = (&service{exe: f.exe}).execute(&entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_MOVE,
		Sources: []*entity.FileOperationRef{f.ref(t, "keep.txt")}, Destination: f.ref(t, ".trash")},
		false, &operationStream{ctx: context.Background()})
	require.ErrorContains(t, err, "Trash")
	require.FileExists(t, filepath.Join(f.root, "keep.txt"))
}

func TestNamedMethodsShareTheOrganizationEngine(t *testing.T) {
	// Named RPC handlers preserve the same request-bound stream for physical and logical scopes.
	for _, logical := range []bool{false, true} {
		t.Run(map[bool]string{false: "Location", true: "Library"}[logical], func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			root := f.ref(t, "")
			if logical {
				root = libraryRef(0)
			}
			created := &operationStream{ctx: ctx}
			require.NoError(t, Mkdir(f.exe, &entity.MkdirFilesRequest{Destination: root, Name: "new"}, created))
			require.EqualValues(t, 1, created.summary().SucceededCount)
			var source *entity.FileOperationRef
			if logical {
				source = libraryRef(created.updates[1].Entry.GetFileId())
			} else {
				source = f.ref(t, "new")
				root = f.ref(t, "")
			}

			// Rename, then remove through the same backend primitives selected by reference type.
			moved := &operationStream{ctx: ctx}
			require.NoError(t, Move(f.exe, &entity.MoveFilesRequest{Sources: []*entity.FileOperationRef{source}, Destination: root, Name: "renamed"}, moved))
			require.EqualValues(t, 1, moved.summary().SucceededCount)
			if !logical {
				source = f.ref(t, "renamed")
			}
			removed := &operationStream{ctx: ctx}
			require.NoError(t, Remove(f.exe, &entity.RemoveFilesRequest{Sources: []*entity.FileOperationRef{source}}, removed))
			require.EqualValues(t, 1, removed.summary().SucceededCount)
		})
	}
}

func TestLibraryTrashFileCannotBeRemovedAgain(t *testing.T) {
	// Ordinary removal remains logical; a repeated request cannot permanently delete the retained File.
	f := setup(t)
	f.write(t, "source.txt", "keep")
	file := f.original(t, "source.txt")
	spec := &entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE,
		Sources: []*entity.FileOperationRef{libraryRef(file.ID)}}
	f.execute(t, spec, nil)
	stream := f.execute(t, spec, nil)
	require.EqualValues(t, 1, stream.summary().FailedCount)
	require.Contains(t, stream.updates[1].Entry.Error, "cannot be removed")
	require.FileExists(t, filepath.Join(f.root, "source.txt"))
	stored, err := f.exe.Lib().GetFile(context.Background(), file.ID)
	require.NoError(t, err)
	require.Equal(t, file.Note, stored.Note)
}

func TestFileOperationDryRunReportsThePlanWithoutChanges(t *testing.T) {
	// A dry run resolves the same plan and reports it as UNPROCESSED work.
	f := setup(t)
	f.write(t, "folder/a.txt", "a")
	stream := &operationStream{ctx: context.Background()}
	require.NoError(t, (&service{exe: f.exe}).execute(&entity.FileOperationSpec{Kind: entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE,
		Sources: []*entity.FileOperationRef{f.ref(t, "folder")}}, true, stream))

	require.FileExists(t, filepath.Join(f.root, "folder", "a.txt"))
	require.NoDirExists(t, filepath.Join(f.root, ".trash"))
	final := stream.summary()
	require.True(t, final.GetCompleted())
	require.True(t, final.GetDryrun())
	require.Zero(t, final.GetSucceededCount())
	require.Equal(t, final.GetTotalItemCount(), final.GetUnprocessedCount())
	entries := 0
	for _, update := range stream.updates {
		if update.GetEntry() != nil {
			entries++
			require.Equal(t, entity.FileOperationOutcome_FILE_OPERATION_OUTCOME_UNPROCESSED, update.GetEntry().GetOutcome())
		}
	}
	require.NotZero(t, entries)
}
