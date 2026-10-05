package library

import (
	"bytes"
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestMergeFilesZipsSavedHistoryAndRetainsSourceIdentity(t *testing.T) {
	// Different owners share some history, with a distinct historical version and evidence dates.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	target, source := &File{Name: "same", Note: "target"}, &File{Name: "source", Note: "source"}
	createFileRows(t, db, target, source)
	first, last := int64(10), int64(30)
	preferred := &FileVersion{FileID: target.ID, Signature: []byte("shared"), Hash: bytes.Repeat([]byte{2}, 32), Size: 3, Mode: 0600, FirstArchivedAtNS: &first, LastArchivedAtNS: &first}
	other := &FileVersion{FileID: source.ID, Signature: []byte("shared"), Hash: bytes.Repeat([]byte{1}, 32), Size: 3, Mode: 0644, FirstArchivedAtNS: &last, LastArchivedAtNS: &last}
	unique := &FileVersion{FileID: source.ID, Signature: []byte("old"), Size: 2, Mode: 0644}
	require.NoError(t, db.Create(preferred).Error)
	require.NoError(t, db.Create(other).Error)
	require.NoError(t, db.Create(unique).Error)
	middle := int64(20)
	require.NoError(t, recordVersionArchives(db, other.ID, &middle))

	// Merge uses existing metadata semantics without inventing an archive at the current time.
	require.NoError(t, lib.MergeFiles(ctx, target.ID, []int64{source.ID}))
	stored, err := lib.GetFileVersion(ctx, preferred.ID)
	require.NoError(t, err)
	require.Equal(t, uint32(0600), stored.Mode)
	require.Equal(t, preferred.Hash, stored.Hash)
	require.Equal(t, &first, stored.FirstArchivedAtNS)
	require.Equal(t, &last, stored.LastArchivedAtNS)
	var dates []int64
	require.NoError(t, db.Model(&FileVersionArchive{}).Where("version_id = ?", preferred.ID).Order("archived_at_ns").Pluck("archived_at_ns", &dates).Error)
	require.Equal(t, []int64{10, 20, 30}, dates)
	moved, err := lib.GetFileVersion(ctx, unique.ID)
	require.NoError(t, err)
	require.Equal(t, target.ID, moved.FileID)
	retained, err := lib.GetFile(ctx, source.ID)
	require.NoError(t, err)
	require.NotZero(t, retained.ParentID)
	target, err = lib.GetFile(ctx, target.ID)
	require.NoError(t, err)
	require.Equal(t, "target\n\nsource", target.Note)
}

func TestMergeKeepsVersionOwnership(t *testing.T) {
	// A File retains its independent identity after removing one saved version.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	a, b := &File{Name: "a"}, &File{Name: "b"}
	createFileRows(t, db, a, b)
	av := &FileVersion{FileID: a.ID, Signature: []byte("same"), Size: 1}
	require.NoError(t, db.Create(av).Error)

	// Metadata maintenance is transactional; version ownership is what rejects the wrong File.
	_, err := lib.RemoveFileVersion(ctx, b.ID, av.ID, false)
	require.Error(t, err)
	_, err = lib.RemoveFileVersion(ctx, a.ID, av.ID, false)
	require.NoError(t, err)
	require.NoError(t, lib.MergeFiles(ctx, a.ID, []int64{b.ID}))
	_, err = lib.GetFile(ctx, a.ID)
	require.NoError(t, err)
}

func TestFrozenRestoreAndFileImportAfterVersionRemoval(t *testing.T) {
	// Freeze trusted Job content before deleting its mutable catalog version.
	ctx := context.Background()
	db, lib, _, file, publication := restorePublicationFixture(t)
	version, err := lib.GetFileVersion(ctx, publication.Result.SourceVersionID)
	require.NoError(t, err)
	publication.Version = version
	_, err = lib.RemoveFileVersion(ctx, file.ID, version.ID, false)
	require.NoError(t, err)

	// Frozen version facts let recovery create an independent File.
	result, err := lib.PublishRestore(ctx, publication)
	require.NoError(t, err)
	require.Equal(t, RestoreNewFile, result.Outcome)
	require.NotEqual(t, file.ID, result.ResultFileID)
	require.Equal(t, file.ID, result.SourceFileID)
	require.Equal(t, version.ID, result.SourceVersionID)
	_, err = lib.PublishRestore(ctx, publication)
	require.ErrorContains(t, err, "already linked")
	var restored FileVersion
	require.NoError(t, db.Where("file_id = ?", result.ResultFileID).First(&restored).Error)
	_, err = lib.RemoveFileVersion(ctx, result.ResultFileID, restored.ID, false)
	require.NoError(t, err)

	// Restored organization and its current association survive metadata export/import.
	snapshot := exportRestoreImportSnapshot(t, lib)
	importedDB, imported := newTestLibrary(t)
	require.NoError(t, imported.Import(ctx, bytes.NewReader(snapshot), false))
	restoredFile, err := imported.GetFile(ctx, result.ResultFileID)
	require.NoError(t, err)
	require.Equal(t, result.ResultFileID, restoredFile.ID)
	require.False(t, importedDB.Migrator().HasTable("restore_results"))
}

func TestTrimAndMediaDeleteDryRunsReportWithoutRemoving(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "8d0f4c22-9b31-4a6e-8f5d-2c7e1a9b3d40", Name: "Reported",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "saved.txt", Signature: []byte("saved"), Mode: 0o644}).Error)
	createFileRows(t, db, &File{Name: "orphan.txt", Kind: entity.FileKind_FILE_KIND_REGULAR})

	// The trim report counts the same rows the real trim removes.
	preview, err := lib.Trim(ctx, true, true, true)
	require.NoError(t, err)
	require.Equal(t, int64(1), preview.Files)
	var files int64
	require.NoError(t, db.Model(ModelFile).Count(&files).Error)
	require.Equal(t, int64(1), files)

	// Deleting reports the Media and its recorded Positions before removing either.
	report, err := lib.DeleteMedia(ctx, true, media.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), report.Media)
	require.Equal(t, int64(1), report.Positions)
	var positions int64
	require.NoError(t, db.Model(ModelPosition).Count(&positions).Error)
	require.Equal(t, int64(1), positions)
	require.NoError(t, db.First(new(Media), media.ID).Error)

	// The real calls then remove exactly what the reports announced.
	applied, err := lib.DeleteMedia(ctx, false, media.ID)
	require.NoError(t, err)
	require.Equal(t, report, applied)
	require.Error(t, db.First(new(Media), media.ID).Error)
}
