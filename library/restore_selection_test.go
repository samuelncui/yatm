package library

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func restoreSelection(fileID int64) *entity.FileSelection {
	return &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: fileID}},
		Scope: entity.FileScope_FILE_SCOPE_ALL}
}

func savedSelectionVersion(t *testing.T, db *gorm.DB, fileID, savedAt, size int64) *FileVersion {
	t.Helper()
	version := &FileVersion{FileID: fileID, Signature: []byte(fmt.Sprintf("%d-%d", fileID, savedAt)),
		Hash: bytes.Repeat([]byte{1}, 32), Size: size, Mode: 0644, FirstArchivedAt: &savedAt, LastArchivedAt: &savedAt}
	require.NoError(t, db.Create(version).Error)
	return version
}

func TestRestoreSelectionExplicitVersionsOverrideWholeFilePolicy(t *testing.T) {
	// Give one file multiple saved contents and select it both directly and through its parent.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	directory := &File{Name: "folder", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, lib.SaveFile(ctx, directory))
	file := &File{Name: "report.txt", ParentID: directory.ID, Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, lib.SaveFile(ctx, file))
	first := savedSelectionVersion(t, db, file.ID, 100, 5)
	second := savedSelectionVersion(t, db, file.ID, 200, 9)
	third := savedSelectionVersion(t, db, file.ID, 300, 14)
	cutoff := int64(250)
	request := &entity.InspectSelectionRequest{Restore: true,
		Selections:     []*entity.FileSelection{restoreSelection(directory.ID), restoreSelection(file.ID), restoreSelection(directory.ID)},
		FileVersionIds: []int64{first.ID, third.ID, first.ID}, VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtMs: &cutoff}}

	// Explicit overrides may lie after the cutoff and several versions of the same File are intentional.
	reply, err := lib.InspectSelection(ctx, request)
	require.NoError(t, err)
	require.EqualValues(t, 2, reply.Files)
	require.EqualValues(t, 19, reply.Bytes)
	require.EqualValues(t, 2, reply.MissingCopies)
	require.Zero(t, reply.UnmatchedVersions)
	require.Len(t, reply.ResolvedVersions, 2)
	require.Equal(t, first.ID, reply.ResolvedVersions[0].Version.Id)
	require.Equal(t, third.ID, reply.ResolvedVersions[1].Version.Id)

	// Removing overrides restores the automatic cutoff policy without double-counting overlapping roots.
	request.FileVersionIds = nil
	reply, err = lib.InspectSelection(ctx, request)
	require.NoError(t, err)
	require.EqualValues(t, 1, reply.Files)
	require.EqualValues(t, 9, reply.Bytes)
	require.Len(t, reply.ResolvedVersions, 1)
	require.Equal(t, second.ID, reply.ResolvedVersions[0].Version.Id)
}

func TestRestoreSelectionUnmatchedIsNotMissingCopy(t *testing.T) {
	// Distinguish no history, only later evidence, and an eligible version whose bytes are unavailable.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	files := make([]*File, 0, 3)
	for _, name := range []string{"unsaved.txt", "later.txt", "missing-copy.txt"} {
		file := &File{Name: name, Kind: entity.FileKind_FILE_KIND_REGULAR}
		require.NoError(t, lib.SaveFile(ctx, file))
		files = append(files, file)
	}
	savedSelectionVersion(t, db, files[1].ID, 300, 7)
	savedSelectionVersion(t, db, files[2].ID, 100, 5)
	cutoff := int64(200)
	request := &entity.InspectSelectionRequest{Restore: true, VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtMs: &cutoff}}
	for _, file := range files {
		request.Selections = append(request.Selections, restoreSelection(file.ID))
	}

	// Mismatches remain individually visible but do not contribute fabricated bytes or restore items.
	reply, err := lib.InspectSelection(ctx, request)
	require.NoError(t, err)
	require.EqualValues(t, 1, reply.Files)
	require.EqualValues(t, 5, reply.Bytes)
	require.EqualValues(t, 2, reply.UnmatchedVersions)
	require.Zero(t, reply.SkippedVersions)
	require.EqualValues(t, 1, reply.MissingCopies)
	require.Equal(t, entity.RestoreVersionMatch_RESTORE_VERSION_NO_SAVED_VERSION, reply.ResolvedVersions[0].Match)
	require.Equal(t, entity.RestoreVersionMatch_RESTORE_VERSION_AFTER_CUTOFF, reply.ResolvedVersions[1].Match)

	// Explicit skip consent does not hide missing copies of the matching version.
	request.SkipUnmatchedVersions = true
	reply, err = lib.InspectSelection(ctx, request)
	require.NoError(t, err)
	require.EqualValues(t, 2, reply.UnmatchedVersions)
	require.EqualValues(t, 2, reply.SkippedVersions)
	require.EqualValues(t, 1, reply.MissingCopies)
	require.EqualValues(t, 1, reply.Files)
}

func TestRestoreSelectionBoundsExpandedResolutionReply(t *testing.T) {
	// More descendants than one database page must not become a whole-tree response payload.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	directory := &File{Name: "many", Kind: entity.FileKind_FILE_KIND_DIRECTORY}
	require.NoError(t, lib.SaveFile(ctx, directory))
	for index := 0; index < 125; index++ {
		require.NoError(t, lib.SaveFile(ctx, &File{Name: fmt.Sprintf("%03d.txt", index), ParentID: directory.ID,
			Kind: entity.FileKind_FILE_KIND_REGULAR}))
	}
	cutoff := int64(100)
	request := &entity.InspectSelectionRequest{Restore: true,
		Selections:    []*entity.FileSelection{restoreSelection(directory.ID), restoreSelection(directory.ID)},
		VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtMs: &cutoff}, SkipUnmatchedVersions: true}

	// Overlap counts once, skipped files have no executable content, and descendants remain server-side.
	reply, err := lib.InspectSelection(ctx, request)
	require.NoError(t, err)
	require.EqualValues(t, 125, reply.UnmatchedVersions)
	require.EqualValues(t, 125, reply.SkippedVersions)
	require.Zero(t, reply.Files)
	require.Zero(t, reply.Bytes)
	require.Empty(t, reply.ResolvedVersions)
}

func TestRestoreSelectionRejectsInvalidPoliciesAndOverflow(t *testing.T) {
	// A selected version supplies an otherwise valid input for each malformed policy case.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	file := &File{Name: "huge.txt", Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, lib.SaveFile(ctx, file))
	first := savedSelectionVersion(t, db, file.ID, 100, math.MaxInt64)
	second := savedSelectionVersion(t, db, file.ID, 200, 1)
	negative, zero := int64(-1), int64(0)

	// Consent without a date is invalid; epoch remains an explicitly present date, not latest.
	for _, request := range []*entity.InspectSelectionRequest{
		{Restore: true, FileVersionIds: []int64{first.ID}, SkipUnmatchedVersions: true},
		{Restore: true, FileVersionIds: []int64{first.ID}, VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtMs: &negative}},
		{Selections: []*entity.FileSelection{restoreSelection(file.ID)}, VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtMs: &zero}},
		{Restore: true, FileVersionIds: []int64{first.ID, second.ID}},
	} {
		_, err := lib.InspectSelection(ctx, request)
		require.Error(t, err)
	}
	reply, err := lib.InspectSelection(ctx, &entity.InspectSelectionRequest{Restore: true,
		Selections: []*entity.FileSelection{restoreSelection(file.ID)}, VersionPolicy: &entity.RestoreVersionPolicy{BeforeAtMs: &zero}})
	require.NoError(t, err)
	require.EqualValues(t, 1, reply.UnmatchedVersions)
	require.Zero(t, reply.Files)
}
