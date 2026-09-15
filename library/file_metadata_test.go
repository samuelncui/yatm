package library

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEditFileMetadataSupportsBatchTagOperations(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	require.NoError(t, db.Create([]*File{
		{ID: 1, Name: "one.txt", Mode: 0o644},
		{ID: 2, Name: "two.txt", Mode: 0o644},
	}).Error)

	// Set one shared note and a canonical Tag patch on both Files.
	note := "reviewed"
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{2, 1, 1}, FileMetadataEdit{
		AddTags: []string{" Finance ", "archive", "FINANCE"}, Note: &note,
	}))
	tags, err := lib.MGetFileTags(ctx, 1, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"archive", "finance"}, tags[1])
	require.Equal(t, tags[1], tags[2])
	var notes []string
	require.NoError(t, db.Model(ModelFile).Order("id ASC").Pluck("note", &notes).Error)
	require.Equal(t, []string{"reviewed", "reviewed"}, notes)

	// Apply idempotent additions and removals without disturbing other Tags.
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{1}, FileMetadataEdit{
		AddTags: []string{"favorite", "archive"}, RemoveTags: []string{"finance"},
	}))
	tags, err = lib.MGetFileTags(ctx, 1, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"archive", "favorite"}, tags[1])
	require.Equal(t, []string{"archive", "finance"}, tags[2])

	// An empty optional note and explicit removals clear their respective metadata fields.
	emptyNote := ""
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{2}, FileMetadataEdit{
		RemoveTags: []string{"archive", "finance"}, Note: &emptyNote,
	}))
	stored, err := lib.GetFile(ctx, 2)
	require.NoError(t, err)
	require.Empty(t, stored.Note)
	tags, err = lib.MGetFileTags(ctx, 2)
	require.NoError(t, err)
	require.Empty(t, tags[2])
}

func TestEditFileMetadataRejectsPartialBatch(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	require.NoError(t, db.Create(&File{ID: 1, Name: "one.txt", Mode: 0o644, Note: "original"}).Error)

	// Reject a missing File before any metadata field can change an existing row.
	note := "changed"
	err := lib.EditFileMetadata(ctx, []int64{1, 99}, FileMetadataEdit{AddTags: []string{"tag"}, Note: &note})
	require.ErrorContains(t, err, "id=99")
	file := new(File)
	require.NoError(t, db.First(file, 1).Error)
	require.Equal(t, "original", file.Note)
	tags, err := lib.MGetFileTags(ctx, 1)
	require.NoError(t, err)
	require.Empty(t, tags[1])
}

func TestEditFileMetadataLimitsAddedTagsPerRequest(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	require.NoError(t, db.Create(&File{ID: 1, Name: "one.txt", Mode: 0o644}).Error)

	// Accept the complete per-request allowance after normalizing the Tag set.
	tags := make([]string, maxAddedTagsPerEdit)
	for index := range tags {
		tags[index] = fmt.Sprintf("tag-%03d", index)
	}
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{1}, FileMetadataEdit{AddTags: tags}))

	// Reject a larger addition without changing relations already committed.
	tooMany := append(append([]string(nil), tags...), "one-too-many")
	require.ErrorContains(t, lib.EditFileMetadata(ctx, []int64{1}, FileMetadataEdit{AddTags: tooMany}), "exceeds 16 added Tags")
	stored, err := lib.MGetFileTags(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, tags, stored[1])
}

func TestRemoveFileTagsBatchesIDsAndTags(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestLibrary(t)
	ids := make([]int64, fileTagMutationBatchSize+1)
	tags := make([]string, fileTagMutationBatchSize+1)
	for index := range ids {
		ids[index] = int64(index + 1)
		tags[index] = fmt.Sprintf("tag-%03d", index)
	}

	// Observe the generated deletes without depending on one database's parameter ceiling.
	const callback = "test:count-file-tag-delete-batches"
	var calls, maxParameters int
	require.NoError(t, db.Callback().Delete().After("gorm:delete").Register(callback, func(tx *gorm.DB) {
		calls++
		if len(tx.Statement.Vars) > maxParameters {
			maxParameters = len(tx.Statement.Vars)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Delete().Remove(callback) })

	// Split both dimensions into four bounded statements.
	require.NoError(t, removeFileTags(ctx, db, ids, tags))
	require.Equal(t, 4, calls)
	require.LessOrEqual(t, maxParameters, fileTagMutationBatchSize*2)
}

func TestMoveFileMergesDirectoryMetadata(t *testing.T) {
	// Give the two logical directories distinct annotations before their merge.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	target := &File{ID: 1, Name: "merged", Kind: entity.FileKind_FILE_KIND_DIRECTORY, Note: "target note"}
	container := &File{ID: 2, Name: "source", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	source := &File{ID: 3, ParentID: 2, Name: "merged", Kind: entity.FileKind_FILE_KIND_DIRECTORY, Note: "source note"}
	require.NoError(t, db.Create([]*File{target, container, source}).Error)
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{1}, FileMetadataEdit{AddTags: []string{"one"}}))
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{3}, FileMetadataEdit{AddTags: []string{"two"}}))

	// Merge the source directory into the existing root directory without losing either annotation.
	source.ParentID = 0
	require.NoError(t, lib.MoveFile(ctx, source))
	require.Equal(t, int64(1), source.ID)
	stored := new(File)
	require.NoError(t, db.First(stored, 1).Error)
	require.Equal(t, "target note\n\nsource note", stored.Note)
	tags, err := lib.MGetFileTags(ctx, 1, 3)
	require.NoError(t, err)
	require.Equal(t, []string{"one", "two"}, tags[1])
	require.Empty(t, tags[3])
}

func TestMoveFileNoOpPreservesDirectoryMetadata(t *testing.T) {
	// Persist an annotated directory without depending on a presentation mode.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	directory := &File{ID: 1, Name: "directory", Kind: entity.FileKind_FILE_KIND_DIRECTORY, Note: "keep"}
	require.NoError(t, db.Create(directory).Error)
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{directory.ID}, FileMetadataEdit{AddTags: []string{"kept"}}))

	// Treat an unchanged move as a successful no-op without merging the directory into itself.
	stored, err := lib.GetFile(ctx, directory.ID)
	require.NoError(t, err)
	require.NoError(t, lib.MoveFile(ctx, stored))
	stored, err = lib.GetFile(ctx, directory.ID)
	require.NoError(t, err)
	require.Equal(t, "keep", stored.Note)
	tags, err := lib.MGetFileTags(ctx, directory.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"kept"}, tags[directory.ID])
}

func TestTrimDeletesFileTagsWithUnpositionedFile(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	require.NoError(t, db.Create(&File{ID: 1, Name: "orphan.txt", Mode: 0o644}).Error)
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{1}, FileMetadataEdit{AddTags: []string{"orphan"}}))

	// Trim the unpositioned immutable File and its dependent Tag relations together.
	require.NoError(t, lib.Trim(ctx, true, true))
	var fileCount, tagCount int64
	require.NoError(t, db.Model(ModelFile).Count(&fileCount).Error)
	require.NoError(t, db.Model(ModelFileTag).Count(&tagCount).Error)
	require.Zero(t, fileCount)
	require.Zero(t, tagCount)
}

func TestFileMetadataFollowsIdentityLifecycle(t *testing.T) {
	// Give independently organized Files distinct annotations.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	directory := &File{ID: 1, Name: "directory", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	file := &File{ID: 2, ParentID: 1, Name: "before.txt", Mode: 0o644, Signature: []byte("first")}
	other := &File{ID: 3, Name: "other.txt", Mode: 0o644, Signature: []byte("other")}
	require.NoError(t, db.Create([]*File{directory, file, other}).Error)
	note := "identity note"
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{2}, FileMetadataEdit{AddTags: []string{"kept"}, Note: &note}))

	// Rename and move the same File identity without copying metadata to another File.
	file, err := lib.GetFile(ctx, file.ID)
	require.NoError(t, err)
	file.Name = "after.txt"
	file.ParentID = 0
	require.NoError(t, lib.MoveFile(ctx, file))
	stored, err := lib.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, "identity note", stored.Note)
	tags, err := lib.MGetFileTags(ctx, file.ID, other.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"kept"}, tags[file.ID])
	require.Empty(t, tags[other.ID])

	// Moving into Trash preserves annotations because it is still the same File.
	require.NoError(t, lib.Delete(ctx, []int64{file.ID}))
	stored, err = lib.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.NotZero(t, stored.ParentID)
	tags, err = lib.MGetFileTags(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"kept"}, tags[file.ID])
}

func TestTrashRootMetadataSurvivesLaterDeletes(t *testing.T) {
	// Pre-existing Trash metadata belongs to the stable logical root.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	trash := &File{ID: TrashFileID, Name: ".Trash", Kind: entity.FileKind_FILE_KIND_DIRECTORY, Note: "trash note"}
	file := &File{ID: 1, Name: "file.txt", Mode: 0o644}
	require.NoError(t, db.Create([]*File{trash, file}).Error)
	note := "trash note"
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{TrashFileID}, FileMetadataEdit{AddTags: []string{"trash"}, Note: &note}))

	// Allocating a Trash checkpoint must not recreate the stable root identity with empty metadata.
	require.NoError(t, lib.Delete(ctx, []int64{file.ID}))
	stored, err := lib.GetFile(ctx, TrashFileID)
	require.NoError(t, err)
	require.Equal(t, "trash note", stored.Note)
	tags, err := lib.MGetFileTags(ctx, TrashFileID)
	require.NoError(t, err)
	require.Equal(t, []string{"trash"}, tags[TrashFileID])
}

func TestNewTrashReusesCurrentCheckpoint(t *testing.T) {
	// Create the logical Trash root before preparing timestamped checkpoints.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	require.NoError(t, db.Create(&File{ID: TrashFileID, Name: ".Trash", Kind: entity.FileKind_FILE_KIND_DIRECTORY}).Error)

	// Cover the current second and its immediate neighbors so the call deterministically finds an existing checkpoint.
	now := time.Now()
	for offset := -1; offset <= 1; offset++ {
		_, err := lib.mkdir(ctx, db, TrashFileID, now.Add(time.Duration(offset)*time.Second).Format(time.RFC3339), fs.ModePerm)
		require.NoError(t, err)
	}

	// Reusing a checkpoint retains its parent and directory identity.
	checkpoint, err := lib.newTrash(ctx, db)
	require.NoError(t, err)
	require.Equal(t, int64(TrashFileID), checkpoint.ParentID)
}

func TestTagListCountsReferencesAndDropsUnusedTags(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	require.NoError(t, db.Create([]*File{{ID: 1, Name: "one"}, {ID: 2, Name: "two"}}).Error)
	require.NoError(t, lib.EditFileMetadata(ctx, []int64{1, 2}, FileMetadataEdit{AddTags: []string{"shared"}}))

	page, err := lib.ListTags(ctx, "sha", "", 100)
	require.NoError(t, err)
	require.Len(t, page.Tags, 1)
	require.Equal(t, int64(2), page.Tags[0].FileCount)

	require.NoError(t, lib.EditFileMetadata(ctx, []int64{1, 2}, FileMetadataEdit{RemoveTags: []string{"shared"}}))
	page, err = lib.ListTags(ctx, "sha", "", 100)
	require.NoError(t, err)
	require.Empty(t, page.Tags)
}

func TestFileMetadataEnforcesPublicBounds(t *testing.T) {
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	note := strings.Repeat("界", maxNoteRunes+1)
	require.Error(t, lib.EditFileMetadata(ctx, []int64{1}, FileMetadataEdit{Note: &note}))
	require.Error(t, lib.EditFileMetadata(ctx, []int64{1}, FileMetadataEdit{
		AddTags: []string{strings.Repeat("界", maxTagRunes+1)},
	}))
	require.Error(t, lib.EditFileMetadata(ctx, []int64{1}, FileMetadataEdit{}))
	require.Error(t, lib.EditFileMetadata(ctx, []int64{1}, FileMetadataEdit{
		AddTags: []string{"same"}, RemoveTags: []string{" SAME "},
	}))
	ids := make([]int64, maxFileEditIDs+1)
	for index := range ids {
		ids[index] = int64(index + 1)
	}
	tooMany := "too many"
	require.Error(t, lib.EditFileMetadata(ctx, ids, FileMetadataEdit{Note: &tooMany}))
}
