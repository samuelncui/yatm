package restore

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"github.com/stretchr/testify/require"
)

func TestOutputNameProbesAreTemporaryAndContainNoRegularFiles(t *testing.T) {
	// Capture an original reader before the Restore attempt creates any temporary resource.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	_, file := createMediaFile(t, lib, newTapeMedia("NAMES"), ".", "file.txt", []byte("saved"), nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	location, err := lib.GetOnlineSource(ctx, 1)
	require.NoError(t, err)
	source := &library.Location{ExecutorID: "local", RootPath: location.RootPath}
	_, err = exe.CheckOnlineSource(source)
	require.NoError(t, err)

	// Name-only probes use native lookup but never create the final directory tree or file bytes.
	names, err := newOutputNames(ctx, runner)
	require.NoError(t, err)
	claimed, err := names.claim(ctx, "new/deep/file.txt")
	require.NoError(t, err)
	require.True(t, claimed)
	probe := filepath.Join(location.RootPath, names.name)
	require.DirExists(t, probe)
	require.NoDirExists(t, filepath.Join(location.RootPath, "new"))
	require.True(t, source.Excluded(names.name, true))
	require.True(t, source.Excluded(names.name+"/new/deep/file.txt", false))
	_, err = exe.OnlineRoot(probe)
	require.ErrorContains(t, err, "temporary resource")
	_, err = exe.RestoreOutputPath(ctx, runner.destination, names.name+"/file.txt")
	require.ErrorContains(t, err, "temporary resource")
	require.NoError(t, filepath.WalkDir(probe, func(_ string, entry fs.DirEntry, err error) error {
		require.NoError(t, err)
		require.True(t, entry.IsDir(), "even abandoned probes must not become original file entries")
		return nil
	}))

	// Structural conflicts fail explicitly instead of treating a selected file as another file's directory.
	_, err = names.claim(ctx, "new/deep/file.txt/child.txt")
	require.ErrorContains(t, err, "file/directory")
	require.NoError(t, names.close())
	require.NoDirExists(t, probe)
	require.NoDirExists(t, names.index)
	require.False(t, source.Excluded(names.name, true))
}

func TestFrozenOutputNameAliasesAreRejectedBeforeReindexing(t *testing.T) {
	// Old frozen reservations cannot be silently renamed when the destination treats their names as aliases.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	_, file := createMediaFile(t, lib, newTapeMedia("FROZEN"), ".", "file.txt", []byte("saved"), nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	names, err := newOutputNames(ctx, runner)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, names.close()) })
	_, err = names.directory(exe.Paths().Target)
	require.NoError(t, err)
	rules, err := directoryNameRules(exe.Paths().Target, names.name)
	require.NoError(t, err)
	if !rules[0] {
		t.Skip("destination preserves case-distinct filenames")
	}
	require.NoError(t, runner.db.Create([]*Output{{ItemID: 100, Path: "Collision.txt"}, {ItemID: 101, Path: "collision.txt"}}).Error)
	require.ErrorContains(t, names.rebuild(ctx), "frozen Restore output paths collide")
	var stored []Output
	require.NoError(t, runner.db.Where("item_id >= ?", 100).Order("item_id").Find(&stored).Error)
	require.Equal(t, []Output{{ItemID: 100, Path: "Collision.txt"}, {ItemID: 101, Path: "collision.txt"}}, stored)
	require.NoFileExists(t, filepath.Join(exe.Paths().Target, "Collision.txt"))
}

func TestOutputNameProbeCancellationDoesNotAllocateTargetEntries(t *testing.T) {
	// A canceled attempt must not add even temporary destination entries.
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	_, file := createMediaFile(t, lib, newTapeMedia("CANCEL"), ".", "file.txt", []byte("saved"), nil)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	names, err := newOutputNames(ctx, value.(*jobRestoreRunner))
	require.NoError(t, err)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	_, err = names.claim(canceled, "output.txt")
	require.ErrorIs(t, err, context.Canceled)
	entries, err := os.ReadDir(exe.Paths().Target)
	require.NoError(t, err)
	require.Empty(t, entries)
	require.NoError(t, names.close())
	stored, err := exe.GetJob(ctx, job.ID)
	require.NoError(t, err)
	require.Equal(t, entity.JobStatus_PENDING, stored.Status)
}

func TestRestoreDoesNotAdoptNativeAliasOfAnotherFilesOriginal(t *testing.T) {
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	_, file := createMediaFile(t, lib, newTapeMedia("ALIAS"), ".", "photo.jpg", []byte("saved"), nil)
	// Include parent-directory aliases; checking only the basename would miss this existing owner.
	actual := filepath.Join(exe.Paths().Target, "unforged", "alias", "PHOTO.jpg")
	require.NoError(t, os.MkdirAll(filepath.Dir(actual), 0755))
	require.NoError(t, os.WriteFile(actual, []byte("saved"), 0644))
	info, err := os.Stat(actual)
	require.NoError(t, err)
	alternate, err := os.Stat(filepath.Join(exe.Paths().Target, "Unforged", "ALIAS", "photo.jpg"))
	if os.IsNotExist(err) {
		t.Skip("destination has case-distinct names")
	}
	require.NoError(t, err)
	require.True(t, os.SameFile(info, alternate))
	location, err := lib.GetOnlineSource(ctx, 1)
	require.NoError(t, err)
	hash := sha256.Sum256([]byte("saved"))
	_, err = lib.PublishOnline(ctx, location.ID, location.Revision, 1, func(_ context.Context, yield func(*library.OnlinePosition) error) error {
		return yield(&library.OnlinePosition{Path: "unforged/alias/PHOTO.jpg", Size: 5, Hash: hash[:], Mode: 0644, MtimeNS: info.ModTime().UnixNano()})
	})
	require.NoError(t, err)
	owner, err := lib.GetFileLocationAtPath(ctx, 1, "unforged/alias/PHOTO.jpg")
	require.NoError(t, err)
	require.NotEqual(t, file.ID, owner.FileID)
	job := createRestoreJob(t, exe, file.ID)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobRestoreRunner)
	var copy Copy
	require.NoError(t, runner.db.First(&copy).Error)
	require.Equal(t, "Unforged/ALIAS/"+library.RestoredName("photo.jpg", copy.FileVersionID), copy.TargetPath)
	kept, err := lib.GetFileLocation(ctx, owner.FileID)
	require.NoError(t, err)
	require.Equal(t, owner, kept)
	original, err := lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.Nil(t, original)
}

func TestRestoreNameProbesDoNotVisitUnrelatedReadOnlyOriginalDirectories(t *testing.T) {
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	_, file := createMediaFile(t, lib, newTapeMedia("WRITABLE"), ".", "file.txt", []byte("saved"), nil)
	locked := filepath.Join(exe.Paths().Target, "locked")
	require.NoError(t, os.MkdirAll(locked, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(locked, "private.txt"), []byte("private"), 0644))
	location, err := lib.GetOnlineSource(ctx, 1)
	require.NoError(t, err)
	hash := sha256.Sum256([]byte("private"))
	_, err = lib.PublishOnline(ctx, 1, location.Revision, 1, func(_ context.Context, yield func(*library.OnlinePosition) error) error {
		return yield(&library.OnlinePosition{Path: "locked/private.txt", Size: 7, Hash: hash[:], Mode: 0644})
	})
	require.NoError(t, err)
	require.NoError(t, os.Chmod(locked, 0000))
	t.Cleanup(func() { require.NoError(t, os.Chmod(locked, 0755)) })
	require.NoError(t, os.Mkdir(filepath.Join(exe.Paths().Target, "writable"), 0755))
	version, err := lib.LatestFileVersion(ctx, file.ID)
	require.NoError(t, err)
	job, err := Create(ctx, exe, 0, &entity.RestoreJobSpec{FileVersionIds: []int64{version.ID}, Destination: &entity.RestoreDestination{LocationId: 1, Path: "writable"}})
	require.NoError(t, err)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	names, err := newOutputNames(ctx, value.(*jobRestoreRunner))
	require.NoError(t, err)
	require.NoError(t, names.rebuild(ctx))
	require.NoDirExists(t, filepath.Join(locked, names.name))
	require.NoError(t, names.close())
}

func TestRestoreNameOwnerLookupPagesTargetAncestors(t *testing.T) {
	ctx := context.Background()
	exe, lib := setupTestExecutor(t)
	_, file := createMediaFile(t, lib, newTapeMedia("PAGED"), ".", "file.txt", []byte("saved"), nil)
	location, err := lib.GetOnlineSource(ctx, 1)
	require.NoError(t, err)
	_, err = lib.PublishOnline(ctx, 1, location.Revision, 1, func(_ context.Context, yield func(*library.OnlinePosition) error) error {
		for i := 0; i < batchSize; i++ {
			if err := yield(&library.OnlinePosition{Path: fmt.Sprintf("a%03d.txt", i), Mode: 0644}); err != nil {
				return err
			}
		}
		return yield(&library.OnlinePosition{Path: "z-target/existing.txt", Mode: 0644})
	})
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(exe.Paths().Target, "z-target"), 0755))
	version, err := lib.LatestFileVersion(ctx, file.ID)
	require.NoError(t, err)
	job, err := Create(ctx, exe, 0, &entity.RestoreJobSpec{FileVersionIds: []int64{version.ID}, Destination: &entity.RestoreDestination{LocationId: 1, Path: "z-target"}})
	require.NoError(t, err)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	names, err := newOutputNames(ctx, value.(*jobRestoreRunner))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, names.close()) })
	require.NoError(t, names.rebuild(ctx))
	claimed, err := names.claim(ctx, "existing.txt")
	require.NoError(t, err)
	require.False(t, claimed, "an indexed owner on the second ancestor page keeps its name")
}
