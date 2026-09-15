package archive

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/stretchr/testify/require"
)

func publishArchiveOriginal(t *testing.T, exe *executor.Executor, name string, content []byte) (*library.Location, *library.File, string) {
	// Publish a real ordinary file with the same observed facts as a successful Sync.
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(exe.Paths().Source, name)
	require.NoError(t, os.Mkdir(root, 0755))
	root, err := exe.OnlineRoot(root)
	require.NoError(t, err)
	filename := filepath.Join(root, "original.txt")
	require.NoError(t, os.WriteFile(filename, content, 0644))
	info, err := os.Stat(filename)
	require.NoError(t, err)
	source := &library.Location{Name: name, RootPath: root, ExecutorID: "local"}
	require.NoError(t, exe.Lib().CreateOnlineSource(ctx, source))
	hash := sha256.Sum256(content)
	source, err = exe.Lib().PublishOnline(ctx, source.ID, source.Revision, 1, func(_ context.Context, yield func(*library.OnlinePosition) error) error {
		return yield(&library.OnlinePosition{Path: "original.txt", Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(), Hash: hash[:]})
	})
	require.NoError(t, err)
	rows, err := exe.Lib().OnlineFilesPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	file, err := exe.Lib().GetFile(ctx, rows[0].FileID)
	require.NoError(t, err)
	return source, file, filename
}

func TestLibraryArchiveFreezesOpaqueIdentityAndFollowsOnlyItsOriginal(t *testing.T) {
	// Independent originals never become substitute sources merely because their content matches.
	exe := setupTestExecutor(t, executor.Scripts{})
	ctx := context.Background()
	content := []byte("stable original")
	source, file, first := publishArchiveOriginal(t, exe, "first", content)
	_, duplicate, second := publishArchiveOriginal(t, exe, "second", content)
	require.NotEqual(t, file.ID, duplicate.ID)
	file.Signature, file.Note = []byte{2, 0, 255, 17}, "Keep this organization"
	require.NoError(t, exe.Lib().SaveFile(ctx, file))
	source, err := exe.Lib().PublishOnline(ctx, source.ID, source.Revision, 2, func(_ context.Context, yield func(*library.OnlinePosition) error) error {
		return yield(&library.OnlinePosition{FileID: file.ID, Path: "original.txt", Size: file.Size, Mode: file.Mode, MtimeNS: file.ModTime.UnixNano(), Hash: file.Hash, Signature: file.Signature})
	})
	require.NoError(t, err)
	created, err := (&service{exe: exe}).Create(ctx, &entity.CreateArchiveJobRequest{Spec: &entity.ArchiveJobSpec{FileIds: []int64{file.ParentID, file.ID, file.ID}}})
	require.NoError(t, err)
	waitIndexed(t, exe, created.Job.Id)
	value, err := exe.GetJobRunner(ctx, created.Job.Id)
	require.NoError(t, err)
	r := value.(*jobArchiveRunner)
	var items []*Item
	require.NoError(t, r.db.Find(&items).Error)
	require.Len(t, items, 1)
	item := items[0]
	frozen := item.TargetPath
	require.Equal(t, file.Signature, item.Data.Expected.Signature)

	// A logical rename leaves the frozen target unchanged; an unavailable original cannot borrow the duplicate.
	file.Name = "renamed-logically.txt"
	require.NoError(t, exe.Lib().SaveFile(ctx, file))
	require.NoError(t, os.Rename(first, first+"-unavailable"))
	volumeRoot := filepath.Join(exe.Paths().Volumes[0], "archive")
	require.NoError(t, os.Mkdir(volumeRoot, 0755))
	volume, err := mediapkg.InitializeVolume(volumeRoot, &entity.VolumeMediaProfile{SerialNumber: "online-selected", Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)
	_, err = exe.Lib().CreateMedia(ctx, &library.Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: "Archive", Profile: volume.Marker.Profile.Pack(), CreateTime: volume.Marker.CreatedAt})
	require.NoError(t, err)
	_, err = (&service{exe: exe}).WriteMedia(ctx, &entity.WriteArchiveMediaRequest{Id: created.Job.Id, Target: (&entity.ArchiveVolumeTarget{Uuid: volume.Marker.UUID}).Pack()})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(created.Job.Id) }, 15*time.Second, 10*time.Millisecond)
	require.NoError(t, r.db.First(item, item.ID).Error)
	require.Equal(t, entity.CopyStatus_PENDING, item.Status)
	require.NotEqual(t, second, item.Data.SourcePath)
	// A retry can follow the same File to another Location without losing either source's history.
	movedRoot := filepath.Join(exe.Paths().Source, "relocated")
	require.NoError(t, os.Mkdir(movedRoot, 0755))
	movedRoot, err = exe.OnlineRoot(movedRoot)
	require.NoError(t, err)
	movedPath := filepath.Join(movedRoot, "relocated.txt")
	require.NoError(t, os.Rename(first+"-unavailable", movedPath))
	source, err = exe.Lib().GetOnlineSource(ctx, source.ID)
	require.NoError(t, err)
	_, err = exe.Lib().PublishOnline(ctx, source.ID, source.Revision, 3, func(context.Context, func(*library.OnlinePosition) error) error { return nil })
	require.NoError(t, err)
	relocated := &library.Location{Name: "Relocated", RootPath: movedRoot, ExecutorID: "local"}
	require.NoError(t, exe.Lib().CreateOnlineSource(ctx, relocated))
	moved, err := os.Stat(movedPath)
	require.NoError(t, err)
	_, err = exe.Lib().PublishOnline(ctx, relocated.ID, relocated.Revision, 4, func(_ context.Context, yield func(*library.OnlinePosition) error) error {
		return yield(&library.OnlinePosition{FileID: file.ID, Path: "relocated.txt", Size: moved.Size(), Mode: uint32(moved.Mode()), MtimeNS: moved.ModTime().UnixNano(), Hash: file.Hash, Signature: file.Signature})
	})
	require.NoError(t, err)
	_, err = (&service{exe: exe}).WriteMedia(ctx, &entity.WriteArchiveMediaRequest{Id: created.Job.Id, Target: (&entity.ArchiveVolumeTarget{Uuid: volume.Marker.UUID}).Pack()})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(created.Job.Id) }, 15*time.Second, 10*time.Millisecond)
	require.NoError(t, r.db.First(item, item.ID).Error)
	require.Equal(t, entity.CopyStatus_SUBMITTED, item.Status)
	require.Equal(t, movedPath, item.Data.SourcePath)
	require.Equal(t, source.ID, item.Data.Expected.OriginalLocationId)
	require.Equal(t, frozen, item.TargetPath)
	actual, err := os.ReadFile(filepath.Join(volumeRoot, item.MediaPath))
	require.NoError(t, err)
	require.Equal(t, content, actual)
	for _, locationID := range []int64{source.ID, relocated.ID} {
		jobs, err := exe.ListJob(ctx, &entity.JobFilter{LocationId: &locationID})
		require.NoError(t, err)
		require.Len(t, jobs.Jobs, 1)
		require.Equal(t, created.Job.Id, jobs.Jobs[0].ID)
	}

	// Publication binds the frozen File directly, without constructing another v1 File identity.
	positions, err := exe.Lib().ListMediaFilePositions(ctx, *item.MediaID, "", 100)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	require.Equal(t, file.Signature, positions[0].Signature)
	versions, _, err := exe.Lib().ListFileVersions(ctx, file.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, file.Signature, versions[0].Signature)
	otherVersions, _, err := exe.Lib().ListFileVersions(ctx, duplicate.ID, 0, 10)
	require.NoError(t, err)
	require.Empty(t, otherVersions)
	kept, err := exe.Lib().GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, file.Signature, kept.Signature)
	require.Equal(t, file.Note, kept.Note)
}

func TestLibraryArchiveRejectsActualTransferredContentMismatch(t *testing.T) {
	// Freeze an online identity before an external same-size, same-mtime overwrite.
	exe := setupTestExecutor(t, executor.Scripts{})
	ctx := context.Background()
	_, file, filename := publishArchiveOriginal(t, exe, "source", []byte("before"))
	created, err := (&service{exe: exe}).Create(ctx, &entity.CreateArchiveJobRequest{Spec: &entity.ArchiveJobSpec{FileIds: []int64{file.ID}}})
	require.NoError(t, err)
	waitIndexed(t, exe, created.Job.Id)
	info, err := os.Stat(filename)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filename, []byte("after!"), 0644))
	require.NoError(t, os.Chtimes(filename, info.ModTime(), info.ModTime()))
	volumeRoot := filepath.Join(exe.Paths().Volumes[0], "archive")
	require.NoError(t, os.Mkdir(volumeRoot, 0755))
	volume, err := mediapkg.InitializeVolume(volumeRoot, &entity.VolumeMediaProfile{SerialNumber: "changed-original", Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)
	_, err = exe.Lib().CreateMedia(ctx, &library.Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: "Archive", Profile: volume.Marker.Profile.Pack(), CreateTime: volume.Marker.CreatedAt})
	require.NoError(t, err)
	_, err = (&service{exe: exe}).WriteMedia(ctx, &entity.WriteArchiveMediaRequest{Id: created.Job.Id, Target: (&entity.ArchiveVolumeTarget{Uuid: volume.Marker.UUID}).Pack()})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(created.Job.Id) }, 15*time.Second, 10*time.Millisecond)

	// Real ACP transfer verification must leave the item un-staged and publish no false replica.
	value, err := exe.GetJobRunner(ctx, created.Job.Id)
	require.NoError(t, err)
	var item Item
	require.NoError(t, value.(*jobArchiveRunner).db.First(&item).Error)
	require.Equal(t, entity.CopyStatus_PENDING, item.Status)
	require.Empty(t, item.MediaPath)
	page, err := exe.Lib().SearchFiles(ctx, "has:archive", "", 100)
	require.NoError(t, err)
	require.Empty(t, page.Results)
}
