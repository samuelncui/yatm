//go:build e2e

package e2e

import (
	"context"
	"encoding/base64"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestLocationAnalyzeReadArchive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	registered, err := f.locations.Create(ctx, &entity.CreateLocationRequest{Location: &entity.Location{Name: "Daily", RootPath: root, Config: &entity.LocationConfig{Ignore: &entity.IgnoreRules{Format: "gitignore", Text: "/downloads/\n"}}}})
	require.NoError(t, err)
	runScan := func() {
		created, err := f.scan.Create(ctx, &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{Selections: []*entity.FileSelection{{Target: &entity.FileSelection_Location{Location: &entity.LocationSelection{LocationId: registered.Location.Id}}, Scope: entity.FileScope_FILE_SCOPE_ALL}}, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_PUBLISH_ORIGINALS}})
		require.NoError(t, err)
		waitForVolumeJobCompleted(t, ctx, f, created.Job.Id)
		require.Eventually(t, func() bool { return !f.exe.IsRunning(created.Job.Id) }, 5*time.Second, 10*time.Millisecond)
		progress, err := f.scan.GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: created.Job.Id})
		require.NoError(t, err)
		require.Zero(t, progress.PreviewsFailedCount)
	}
	runScan()
	// A query is a Search: one bounded page over the live directory, continued by its cursor.
	query := []string{"ls", "--location-id", decimal(registered.Location.Id), "--query", "name:*.txt", "--limit", "1",
		"--long", "--status", "--include", "operations"}
	indexed := new(entity.SearchFilesResponse)
	cliResult(t, ctx, f.cli, indexed, query...)
	require.Len(t, indexed.Entries, 1)
	require.NotEmpty(t, indexed.NextCursor)
	next := new(entity.SearchFilesResponse)
	cliResult(t, ctx, f.cli, next, append(append([]string{}, query...), "--cursor", indexed.NextCursor)...)
	require.Len(t, next.Entries, 1)
	fileID, duplicateID := indexed.Entries[0].GetAssociatedFileId(), next.Entries[0].GetAssociatedFileId()
	require.Positive(t, fileID)
	require.Positive(t, duplicateID)
	require.NotEqual(t, fileID, duplicateID, "same-content originals retain independent organization")
	get := func(id int64) *entity.FilesDetail {
		detail, err := f.files.Get(ctx, &entity.GetFileRequest{Reference: &entity.FileOperationRef{Target: &entity.FileOperationRef_FileId{FileId: id}}})
		require.NoError(t, err)
		return detail.Detail
	}
	versions := func(id int64) []*entity.FileVersion {
		page, err := f.files.ListVersions(ctx, &entity.ListFileVersionsRequest{FileId: id, Limit: 10})
		require.NoError(t, err)
		return page.Versions
	}
	detail := get(fileID)
	require.Equal(t, entity.FilesArchive_FILES_ARCHIVE_NONE, detail.Entry.Status.Archive)
	require.Empty(t, versions(fileID))
	encoded, err := protojson.Marshal(detail.ContentReference)
	require.NoError(t, err)
	url := f.filesURL + "/content?ref=" + base64.RawURLEncoding.EncodeToString(encoded)
	requireHTTPNotFound(t, ctx, http.MethodGet, url)
	requireHTTPNotFound(t, ctx, http.MethodHead, url)

	volume, err := f.media.InitializeVolume(ctx, &entity.InitializeVolumeRequest{MountPoint: f.volumeRoot, Name: "Archive", Profile: &entity.VolumeMediaProfile{SerialNumber: "online-e2e", Type: entity.VolumeType_VOLUME_TYPE_HDD}})
	require.NoError(t, err)
	archive, err := f.archive.Create(ctx, &entity.CreateArchiveJobRequest{Spec: &entity.ArchiveJobSpec{Selections: []*entity.FileSelection{{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: fileID}}, Scope: entity.FileScope_FILE_SCOPE_ALL}}}})
	require.NoError(t, err)
	waitForVolumeJobReady(t, ctx, f, archive.Job.Id)
	_, err = f.archive.WriteMedia(ctx, &entity.WriteArchiveMediaRequest{Id: archive.Job.Id, Target: (&entity.ArchiveVolumeTarget{Uuid: volume.Media.Identity}).Pack()})
	require.NoError(t, err)
	waitForVolumeJobCompleted(t, ctx, f, archive.Job.Id)
	archived, err := f.archive.ListFiles(ctx, &entity.ListArchiveJobFilesRequest{Id: archive.Job.Id, Limit: 10})
	require.NoError(t, err)
	require.Len(t, archived.Items, 1)
	data, err := os.ReadFile(filepath.Join(f.volumeRoot, archived.Items[0].File.MediaPath))
	require.NoError(t, err)
	require.Equal(t, content, data)
	first, duplicate := versions(fileID), versions(duplicateID)
	require.Len(t, first, 1)
	require.Len(t, duplicate, 1)
	require.NotEqual(t, first[0].Id, duplicate[0].Id)
	require.Equal(t, first[0].Signature, duplicate[0].Signature)
	require.Equal(t, entity.FilesCoverage_FILES_COVERAGE_COVERED, get(duplicateID).Entry.Status.Current)

	// Editing one original retains its independently saved version and original permissions.
	require.NoError(t, os.WriteFile(filepath.Join(root, "duplicate.txt"), []byte("unarchived editing"), 0600))
	runScan()
	require.Equal(t, duplicate[0].Id, versions(duplicateID)[0].Id)
	require.Equal(t, entity.FilesCoverage_FILES_COVERAGE_UNCOVERED, get(duplicateID).Entry.Status.Current)
	restored, err := f.restore.Create(ctx, &entity.CreateRestoreJobRequest{Spec: &entity.RestoreJobSpec{Destination: restoreDestination(t, ctx, f.cli, f.paths.Target), FileVersionIds: []int64{duplicate[0].Id}}})
	require.NoError(t, err)
	waitForVolumeJobReady(t, ctx, f, restored.Job.Id)
	_, err = f.restore.RestoreMedia(ctx, &entity.RestoreMediaRequest{Id: restored.Job.Id, Target: (&entity.ReadVolumeTarget{Uuid: volume.Media.Identity}).Pack()})
	require.NoError(t, err)
	waitForVolumeJobCompleted(t, ctx, f, restored.Job.Id)
	output := filepath.Join(f.paths.Target, "Unforged", "Daily", "duplicate.txt")
	data, err = os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, content, data)
	info, err := os.Stat(output)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.Equal(t, duplicate[0].MtimeNs, info.ModTime().UnixNano())

	// Unregistration clears executable originals but retains saved content and both sets of bytes.
	source, err := f.locations.Get(ctx, &entity.GetLocationRequest{Id: registered.Location.Id})
	require.NoError(t, err)
	_, err = f.locations.Delete(ctx, &entity.DeleteLocationRequest{Id: source.Location.Id})
	require.NoError(t, err)
	require.Nil(t, get(fileID).Original)
	require.Len(t, versions(fileID), 1)
	copies, err := f.files.ListCopies(ctx, &entity.ListContentCopiesRequest{Signature: first[0].Signature, Limit: 10})
	require.NoError(t, err)
	require.Len(t, copies.Positions, 1)
	require.FileExists(t, filepath.Join(root, "a.txt"))
	require.FileExists(t, filepath.Join(f.volumeRoot, archived.Items[0].File.MediaPath))
}

func TestOnlineVolumeArchiveRestore(t *testing.T) {
	// Use the shared CLI adapter for a Location-to-Volume lifecycle.
	connection, root := startBinaryInstallation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	originals := filepath.Join(root, "originals")
	require.NoError(t, os.WriteFile(filepath.Join(originals, "a.txt"), []byte("online bytes"), 0o600))
	location := new(entity.CreateLocationResponse)
	cliResult(t, ctx, connection, location, "location", "create", "--name", "Daily", "--root", originals)
	volume := new(entity.InitializeVolumeResponse)
	cliResult(t, ctx, connection, volume, "volume", "initialize", filepath.Join(root, "volumes", "disk"), "--name", "Archive", "--type", "hdd")
	archive := new(entity.CreateArchiveJobResponse)
	cliResult(t, ctx, connection, archive, "archive", "create", "--location", decimal(location.Location.Id)+":a.txt")
	waitCLIJob(t, ctx, connection, archive.Job.Id, true)
	cliResult(t, ctx, connection, new(entity.WriteArchiveMediaResponse), "archive", "write", "volume", decimal(archive.Job.Id), "--uuid", volume.Media.Identity)
	waitCLIJob(t, ctx, connection, archive.Job.Id, false)
	detail := new(entity.FilesDetail)
	cliResult(t, ctx, connection, detail, "files", "get", "--location-id", decimal(location.Location.Id), "--path", "a.txt")
	require.NotNil(t, detail.Entry.AssociatedFileId)
	versions := new(entity.ListFileVersionsResponse)
	cliResult(t, ctx, connection, versions, "files", "versions", decimal(*detail.Entry.AssociatedFileId))
	require.Len(t, versions.Versions, 1)
}
