package library

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestImportArchivePositionRootsExpandsDirectoriesInPages(t *testing.T) {
	// Register the Media before recording the selected inventory range.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "9ef04c80-e054-41fc-8d7f-ed195eab9b47", Name: "Archive",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)

	// Include more than one database page, an unsigned descendant and a LIKE wildcard in the root name.
	directory := &Position{MediaID: media.ID, Path: "folder_/", IsDir: true}
	require.NoError(t, db.Create(directory).Error)
	var selectedChild *Position
	for index := 0; index < batchSize+2; index++ {
		position := &Position{MediaID: media.ID, Path: fmt.Sprintf("folder_/%04d.txt", index),
			Signature: []byte(fmt.Sprintf("signature-%04d", index)), Size: int64(index), Mode: 0o644}
		require.NoError(t, db.Create(position).Error)
		if index == 0 {
			selectedChild = position
		}
	}
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "folder_/unknown.txt", Mode: 0o644}).Error)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "folderX/outside.txt", Signature: []byte("outside"), Mode: 0o644}).Error)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "folder_extra/outside.txt", Signature: []byte("outside-prefix"), Mode: 0o644}).Error)

	// Shared parents count once across pages, and a report leaves no catalog or workset behind.
	temp := t.TempDir()
	t.Setenv("TMPDIR", temp)
	preview, err := lib.ImportArchivePositionSelection(ctx, []int64{selectedChild.ID, directory.ID}, nil, true)
	require.NoError(t, err)
	require.EqualValues(t, batchSize+2, preview.Files)
	require.EqualValues(t, 3, preview.Directories)
	var nodes int64
	require.NoError(t, db.Model(ModelFile).Count(&nodes).Error)
	require.Zero(t, nodes)
	entries, err := os.ReadDir(temp)
	require.NoError(t, err)
	require.Empty(t, entries)

	// The selected child is covered by the directory root and must not be imported twice.
	result, err := lib.ImportArchivePositionRoots(ctx, []int64{selectedChild.ID, directory.ID})
	require.NoError(t, err)
	require.Equal(t, int64(batchSize+2), result.Files)
	require.Equal(t, int64(1), result.SkippedFiles)
	require.Equal(t, preview.Directories, result.Directories)
	require.Len(t, result.FileIDs, batchSize+2, "every admitted entry reports the File it created")
	var versions int64
	require.NoError(t, db.Model(new(FileVersion)).Count(&versions).Error)
	require.Equal(t, int64(batchSize+2), versions)
}

func TestImportArchivePositionRootsTreatsDirectoryPathsAsSegments(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "77caf707-2ea7-4a31-8789-f2dc8ce37d60", Name: "Archive",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	directory := &Position{MediaID: media.ID, Path: "foo", IsDir: true}
	require.NoError(t, db.Create(directory).Error)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "foo/inside.txt", Signature: []byte("inside"), Mode: 0o644}).Error)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "foobar/outside.txt", Signature: []byte("outside"), Mode: 0o644}).Error)

	result, err := lib.ImportArchivePositionRoots(ctx, []int64{directory.ID})
	require.NoError(t, err)
	require.Equal(t, int64(1), result.Files)
	var files []*fileRow
	require.NoError(t, db.Where("kind = ?", entity.FileKind_FILE_KIND_REGULAR).Find(&files).Error)
	require.Len(t, files, 1)
	require.Equal(t, "inside.txt", files[0].Name)
}

func TestImportArchivePositionRootsRejectsUnsignedExplicitFileAtomically(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "961b2cf8-902b-48f7-b0b2-44df82124fda", Name: "Archive",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	valid := &Position{MediaID: media.ID, Path: "valid.txt", Signature: []byte("valid"), Mode: 0o644}
	unknown := &Position{MediaID: media.ID, Path: "unknown.txt", Mode: 0o644}
	require.NoError(t, db.Create(valid).Error)
	require.NoError(t, db.Create(unknown).Error)

	// A bad explicit root rejects the complete request instead of publishing a partial logical import.
	_, err = lib.ImportArchivePositionRoots(ctx, []int64{valid.ID, unknown.ID})
	require.ErrorContains(t, err, "has no archived content identity")
	var versions int64
	require.NoError(t, db.Model(new(FileVersion)).Count(&versions).Error)
	require.Zero(t, versions)
}

func TestImportArchivePositionSelectionDryRunReportsWithoutWriting(t *testing.T) {
	// Record signed and unsigned inventory beneath one selected directory.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "2f0a6bd8-2f1b-4a53-9f0c-6b0f3f9a1c2d", Name: "Archive",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	directory := &Position{MediaID: media.ID, Path: "folder/", IsDir: true}
	require.NoError(t, db.Create(directory).Error)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "folder/saved.txt", Signature: []byte("saved"), Mode: 0o644}).Error)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "folder/second.txt", Signature: []byte("second"), Mode: 0o644}).Error)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "folder/unknown.txt", Mode: 0o644}).Error)

	// The report must match the real admission while leaving every table untouched.
	preview, err := lib.ImportArchivePositionSelection(ctx, []int64{directory.ID}, nil, true)
	require.NoError(t, err)
	require.Equal(t, int64(2), preview.Files)
	require.Equal(t, int64(1), preview.SkippedFiles)
	require.Equal(t, int64(3), preview.Directories)
	var files, versions, nodes int64
	require.NoError(t, db.Model(ModelFile).Count(&files).Error)
	require.NoError(t, db.Model(new(FileVersion)).Count(&versions).Error)
	require.NoError(t, db.Model(ModelFile).Where("kind = ?", entity.FileKind_FILE_KIND_DIRECTORY).Count(&nodes).Error)
	require.Zero(t, files+versions+nodes)

	// Apply the same selection and retain its exact catalog cardinality.
	applied, err := lib.ImportArchivePositionSelection(ctx, []int64{directory.ID}, nil, false)
	require.NoError(t, err)
	require.Equal(t, preview.Files, applied.Files)
	require.Equal(t, preview.SkippedFiles, applied.SkippedFiles)
	require.Equal(t, preview.Directories, applied.Directories)
	require.NoError(t, db.Model(ModelFile).Count(&files).Error)
	require.NoError(t, db.Model(new(FileVersion)).Count(&versions).Error)
	require.Equal(t, int64(5), files)
	require.Equal(t, int64(2), versions)

	// Admitting the same recorded content again adds nothing and reports it as existing.
	repeated, err := lib.ImportArchivePositionSelection(ctx, []int64{directory.ID}, nil, false)
	require.NoError(t, err)
	require.Zero(t, repeated.Files)
	require.Equal(t, int64(2), repeated.ExistingFiles)
	require.Zero(t, repeated.Directories)
	require.Empty(t, repeated.FileIDs)
	require.Zero(t, repeated.SkippedUnsigned, "a signed existing descendant is not an unsigned-only selection")
	var repeatedFiles, repeatedVersions int64
	require.NoError(t, db.Model(ModelFile).Count(&repeatedFiles).Error)
	require.NoError(t, db.Model(new(FileVersion)).Count(&repeatedVersions).Error)
	require.Equal(t, files, repeatedFiles)
	require.Equal(t, versions, repeatedVersions)
}

func TestImportArchivePositionSelectionScopesByMedia(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	selected, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "5c2f1de0-0f4e-4c6f-9f1c-2b8f0d5a7e31", Name: "Selected",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	other, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "8a17b6c4-9d2e-4f31-8a5b-1c9e0f2d3a45", Name: "Other",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	require.NoError(t, db.Create(&Position{MediaID: selected.ID, Path: "kept/", IsDir: true}).Error)
	require.NoError(t, db.Create(&Position{MediaID: selected.ID, Path: "kept/file.txt", Signature: []byte("kept"), Mode: 0o644}).Error)
	outside := &Position{MediaID: other.ID, Path: "outside.txt", Signature: []byte("outside"), Mode: 0o644}
	require.NoError(t, db.Create(outside).Error)

	// A root from another Media rejects the request instead of silently narrowing the selection.
	_, err = lib.ImportArchivePositionSelection(ctx, []int64{outside.ID}, &selected.ID, false)
	require.ErrorContains(t, err, "belongs to another Media")
	var files int64
	require.NoError(t, db.Model(ModelFile).Where("kind = ?", entity.FileKind_FILE_KIND_REGULAR).Count(&files).Error)
	require.Zero(t, files)

	// Without an explicit root the Media's own top-level directories supply the selection.
	applied, err := lib.ImportArchivePositionSelection(ctx, nil, &selected.ID, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), applied.Files)
}

func TestImportArchivePositionSelectionRejectsUnusableMediaSelection(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "d4c9a1f7-3b52-4e8a-9c60-7f1d2e3b4a58", Name: "Empty",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)

	// An invalid scope, empty selection and Media without recorded roots are all explicit errors.
	invalid := int64(0)
	_, err = lib.ImportArchivePositionSelection(ctx, nil, &invalid, true)
	require.ErrorContains(t, err, "Media ID is invalid")
	_, err = lib.ImportArchivePositionSelection(ctx, nil, nil, true)
	require.ErrorContains(t, err, "select between 1 and 1000 archive positions")
	_, err = lib.ImportArchivePositionSelection(ctx, nil, &media.ID, true)
	require.ErrorContains(t, err, "no recorded top-level directory")
	var files int64
	require.NoError(t, db.Model(ModelFile).Count(&files).Error)
	require.Zero(t, files)
}

func TestImportArchivePositionSelectionSkipsExplicitlyAdmittedContent(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "0f0d1c2b-3a4e-4d5f-8b6a-7c8d9e0f1a2b", Name: "Offline Disk",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "manual/", IsDir: true}).Error)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "manual/added.txt", Signature: []byte("added"), Size: 5, Mode: 0o644}).Error)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "dataset/", IsDir: true}).Error)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "dataset/changed.txt", Signature: []byte("changed"), Size: 6, Mode: 0o644}).Error)
	manualRoot := new(Position)
	require.NoError(t, db.Where("media_id = ? AND path = ?", media.ID, "manual/").First(manualRoot).Error)

	first, err := lib.ImportArchivePositionSelection(ctx, []int64{manualRoot.ID}, &media.ID, false)
	require.NoError(t, err)
	require.Equal(t, int64(1), first.Files)

	// The Media-scoped admission must recognize the File the explicit root just created.
	second, err := lib.ImportArchivePositionSelection(ctx, nil, &media.ID, true)
	require.NoError(t, err)
	require.Equal(t, int64(1), second.ExistingFiles)
	require.Equal(t, int64(1), second.Files)
	var files []*fileRow
	require.NoError(t, db.Where("kind = ?", entity.FileKind_FILE_KIND_REGULAR).Order("name").Find(&files).Error)
	names := make([]string, 0, len(files))
	for _, file := range files {
		names = append(names, file.Name)
	}
	require.Equal(t, []string{"added.txt"}, names)
}

func TestImportArchivePositionSelectionReusesCollisionPaths(t *testing.T) {
	for _, parentCollision := range []bool{false, true} {
		t.Run(map[bool]string{false: "directory at leaf", true: "regular File at parent"}[parentCollision], func(t *testing.T) {
			// Existing organization requires the ordinary import naming rules to choose a suffix.
			ctx := context.Background()
			db, lib := newTestLibrary(t)
			media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
				Identity: "31d01aad-8835-4412-a1ca-b6f1f658b154", Name: "Archive",
				Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
			require.NoError(t, err)
			parent, err := lib.MkdirAll(ctx, Root.ID, "Unforged/Archive", 0755)
			require.NoError(t, err)
			physicalPath, logicalPath := "saved.txt", "Unforged/Archive/saved (1).txt"
			if parentCollision {
				require.NoError(t, lib.SaveFile(ctx, &File{ParentID: parent.ID, Name: "folder", Kind: entity.FileKind_FILE_KIND_REGULAR}))
				physicalPath, logicalPath = "folder/saved.txt", "Unforged/Archive/folder (1)/saved.txt"
			} else {
				_, err := lib.MkdirAll(ctx, parent.ID, "saved.txt", 0755)
				require.NoError(t, err)
			}
			copy := &Position{MediaID: media.ID, Path: physicalPath, Signature: []byte("saved"), Size: 5, Mode: 0644}
			require.NoError(t, db.Create(copy).Error)

			// A directory is not saved content; the first admission must create the selected File.
			preview, err := lib.ImportArchivePositionSelection(ctx, []int64{copy.ID}, nil, true)
			require.NoError(t, err)
			require.EqualValues(t, 1, preview.Files)
			require.Zero(t, preview.ExistingFiles)
			if parentCollision {
				require.EqualValues(t, 1, preview.Directories)
			} else {
				require.Zero(t, preview.Directories)
			}
			applied, err := lib.ImportArchivePositionSelection(ctx, []int64{copy.ID}, nil, false)
			require.NoError(t, err)
			require.EqualValues(t, 1, applied.Files)
			require.Len(t, applied.FileIDs, 1)
			require.Equal(t, preview.Directories, applied.Directories)
			file, err := lib.GetFile(ctx, applied.FileIDs[0])
			require.NoError(t, err)
			paths, err := lib.ReadFilePaths(ctx, []*File{file})
			require.NoError(t, err)
			require.Equal(t, "/"+logicalPath, paths[file.ID])

			// Report and write resolve the same suffixed path without admitting another copy.
			for _, dryRun := range []bool{true, false} {
				repeated, err := lib.ImportArchivePositionSelection(ctx, []int64{copy.ID}, nil, dryRun)
				require.NoError(t, err)
				require.Zero(t, repeated.Files)
				require.EqualValues(t, 1, repeated.ExistingFiles)
				require.Empty(t, repeated.FileIDs)
				require.Zero(t, repeated.Directories)
			}
			var versions int64
			require.NoError(t, db.Model(&FileVersion{}).Count(&versions).Error)
			require.EqualValues(t, 1, versions)
		})
	}
}

func TestImportArchivePositionSelectionRollsBackLaterPageFailure(t *testing.T) {
	// A selection spanning inventory pages must retain its original catalog on late failure.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "35c91923-4a6c-48d7-bd21-059865297677", Name: "Archive",
		Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	directory := &Position{MediaID: media.ID, Path: "folder/", IsDir: true}
	require.NoError(t, db.Create(directory).Error)
	for index := 0; index <= batchSize; index++ {
		require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: fmt.Sprintf("folder/%04d.txt", index),
			Signature: []byte(fmt.Sprintf("saved-%04d", index)), Size: int64(index), Mode: 0644}).Error)
	}

	// Fail saved-content publication only after the first complete page has been admitted.
	callback, attempts := "test:archive-import-late-failure", 0
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Name == "FileVersion" {
			attempts++
			if attempts > batchSize {
				tx.AddError(fmt.Errorf("injected late archive admission failure"))
			}
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	result, err := lib.ImportArchivePositionSelection(ctx, []int64{directory.ID}, nil, false)
	require.ErrorContains(t, err, "injected late archive admission failure")
	require.Nil(t, result, "rolled-back File IDs must not escape the transaction")
	require.Equal(t, batchSize+1, attempts)

	// Rollback removes all new organization and versions while preserving physical inventory facts.
	for _, model := range []any{ModelFile, &FileVersion{}, &FileVersionArchive{}} {
		var count int64
		require.NoError(t, db.Model(model).Count(&count).Error)
		require.Zero(t, count)
	}
	var positions int64
	require.NoError(t, db.Model(ModelPosition).Where("media_id = ?", media.ID).Count(&positions).Error)
	require.EqualValues(t, batchSize+2, positions)
}
