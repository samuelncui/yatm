//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestLocationAnalyzeReadArchive(t *testing.T) {
	// Register an actual ordinary directory and exclude its continuously written subtree through RPC.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f := newVolumeE2EFixture(t, ctx)
	root := filepath.Join(f.paths.Source, "daily")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "downloads"), 0755))
	content := []byte("online original content")
	for _, name := range []string{"a.txt", "duplicate.txt", "downloads/active.part"} {
		mode := os.FileMode(0644)
		if name == "duplicate.txt" {
			mode = 0600
		}
		require.NoError(t, os.WriteFile(filepath.Join(root, name), content, mode))
	}
	registered, err := f.online.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Daily", RootPath: root, Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "/downloads/\n"}}})
	require.NoError(t, err)
	require.Equal(t, entity.OnlineBinding_CONFIRMED, registered.Location.Binding)
	created, err := f.sync.Create(ctx, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{LocationId: registered.Location.Id, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS}})
	require.NoError(t, err)
	waitForVolumeJobCompleted(t, ctx, f, created.Job.Id)
	require.Eventually(t, func() bool { return !f.exe.IsRunning(created.Job.Id) }, 5*time.Second, 10*time.Millisecond)
	progress, err := f.sync.GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: created.Job.Id})
	require.NoError(t, err)
	require.EqualValues(t, 2, progress.Added)
	require.Zero(t, progress.PreviewsFailed)
	indexed, err := f.online.ListEntries(ctx, &entity.ListLocationEntriesRequest{LocationId: registered.Location.Id, NameFilter: ".txt", Limit: 1})
	require.NoError(t, err)
	require.True(t, indexed.HasMore)
	next, err := f.online.ListEntries(ctx, &entity.ListLocationEntriesRequest{LocationId: registered.Location.Id, NameFilter: ".txt", Limit: 1, Cursor: indexed.NextCursor})
	require.NoError(t, err)
	require.NotEqual(t, indexed.Entries[0].Original.FileId, next.Entries[0].Original.FileId, "copies have independent organization")

	// Read only the requested observed identity, using Range without a Restore Job.
	position := indexed.Entries[0].Original
	detail, err := f.service.FileGet(ctx, &entity.FileGetRequest{Id: position.FileId})
	require.NoError(t, err)
	require.NotEmpty(t, position.Signature)
	url := fmt.Sprintf("%s/originals/%d?location_id=%d&revision=%d", f.filesURL, detail.File.Id, position.LocationId, indexed.Revision)
	require.Equal(t, content, readHTTPContent(t, ctx, url))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	require.NoError(t, err)
	request.Header.Set("Range", "bytes=0-5")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	data, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	require.Equal(t, http.StatusPartialContent, response.StatusCode)
	require.Equal(t, content[:6], data)
	before, err := f.catalog.GetState(ctx, &entity.GetFileStateRequest{FileId: detail.File.Id})
	require.NoError(t, err)
	require.Zero(t, before.ArchivedCopies)
	require.Nil(t, before.LatestVersion)

	// Archive a selected Library File through the existing mounted Volume and publication checkpoints.
	volume, err := f.service.VolumeInitialize(ctx, &entity.VolumeInitializeRequest{MountPoint: f.volumeRoot, Name: "Archive", Profile: &entity.VolumeMediaProfile{SerialNumber: "online-e2e", Type: entity.VolumeType_VOLUME_TYPE_HDD}})
	require.NoError(t, err)
	archive, err := f.archive.Create(ctx, &entity.CreateArchiveJobRequest{Spec: &entity.ArchiveJobSpec{FileIds: []int64{detail.File.Id}}})
	require.NoError(t, err)
	waitForVolumeJobPending(t, ctx, f, archive.Job.Id)
	_, err = f.archive.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{Id: archive.Job.Id, Target: (&entity.ArchiveVolumeTarget{Uuid: volume.Media.Identity}).Pack()})
	require.NoError(t, err)
	waitForVolumeJobCompleted(t, ctx, f, archive.Job.Id)
	archived, err := f.archive.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{Id: archive.Job.Id, Limit: 10})
	require.NoError(t, err)
	require.Len(t, archived.Items, 1)
	data, err = os.ReadFile(filepath.Join(f.volumeRoot, archived.Items[0].File.MediaPath))
	require.NoError(t, err)
	require.Equal(t, content, data)
	detail, err = f.service.FileGet(ctx, &entity.FileGetRequest{Id: detail.File.Id})
	require.NoError(t, err)
	after, err := f.catalog.GetState(ctx, &entity.GetFileStateRequest{FileId: detail.File.Id})
	require.NoError(t, err)
	require.NotNil(t, after.LatestVersion)
	require.EqualValues(t, 1, after.ArchivedCopies)
	duplicate, err := f.catalog.GetState(ctx, &entity.GetFileStateRequest{FileId: next.Entries[0].Original.FileId})
	require.NoError(t, err)
	require.NotNil(t, duplicate.LatestVersion)
	require.NotEqual(t, after.LatestVersion.Id, duplicate.LatestVersion.Id)
	require.Equal(t, after.LatestVersion.Signature, duplicate.LatestVersion.Signature)
	require.EqualValues(t, 1, duplicate.ArchivedCopies)

	// A later edit retains the other File's saved content, despite never archiving that File directly.
	require.NoError(t, os.WriteFile(filepath.Join(root, "duplicate.txt"), []byte("unarchived editing"), 0600))
	synced, err := f.sync.Create(ctx, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{LocationId: registered.Location.Id, ResultPolicy: entity.ScanResultPolicy_PUBLISH_ORIGINALS}})
	require.NoError(t, err)
	waitForVolumeJobCompleted(t, ctx, f, synced.Job.Id)
	require.Eventually(t, func() bool { return !f.exe.IsRunning(synced.Job.Id) }, 5*time.Second, 10*time.Millisecond)
	versions, err := f.catalog.ListVersions(ctx, &entity.ListFileVersionsRequest{FileId: duplicate.Original.FileId, Limit: 10})
	require.NoError(t, err)
	require.Len(t, versions.Versions, 1)
	require.Equal(t, duplicate.LatestVersion.Id, versions.Versions[0].Id)
	changed, err := f.catalog.GetState(ctx, &entity.GetFileStateRequest{FileId: duplicate.Original.FileId})
	require.NoError(t, err)
	require.Zero(t, changed.ArchivedCopies)

	// Restore that saved version from the shared copy using its own logical path and original mode.
	restored, err := f.restore.Create(ctx, &entity.CreateRestoreJobRequest{
		Spec: &entity.RestoreJobSpec{Destination: restoreDestination(t, ctx, f.cli, f.paths.Target), FileVersionIds: []int64{duplicate.LatestVersion.Id}},
	})
	require.NoError(t, err)
	waitForVolumeJobPending(t, ctx, f, restored.Job.Id)
	_, err = f.restore.RestoreMedia(ctx, &entity.RestoreMediaRequest{
		Id: restored.Job.Id, Target: (&entity.ReadVolumeTarget{Uuid: volume.Media.Identity}).Pack(),
	})
	require.NoError(t, err)
	waitForVolumeJobCompleted(t, ctx, f, restored.Job.Id)
	output := filepath.Join(f.paths.Target, "Unforged", "Daily", "duplicate.txt")
	data, err = os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, content, data)
	info, err := os.Stat(output)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.Equal(t, duplicate.LatestVersion.MtimeNs, info.ModTime().UnixNano())

	// Unregistration removes only online metadata, leaving both original and archived bytes intact.
	source, err := f.online.Get(ctx, &entity.LocationRef{Id: registered.Location.Id})
	require.NoError(t, err)
	_, err = f.online.Delete(ctx, &entity.LocationRef{Id: source.Location.Id, Revision: source.Location.Revision})
	require.NoError(t, err)
	locations, err := f.catalog.GetState(ctx, &entity.GetFileStateRequest{FileId: detail.File.Id})
	require.NoError(t, err)
	require.Nil(t, locations.Original)
	require.NotNil(t, locations.LatestVersion)
	copies, err := f.catalog.ListCopies(ctx, &entity.ListContentCopiesRequest{Signature: locations.LatestVersion.Signature, Limit: 10})
	require.NoError(t, err)
	require.Len(t, copies.Positions, 1)
	require.FileExists(t, filepath.Join(root, "a.txt"))
	require.FileExists(t, filepath.Join(f.volumeRoot, archived.Items[0].File.MediaPath))
}
