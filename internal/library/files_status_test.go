package library

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestFileStatusFactsAndOriginalDuplicates(t *testing.T) {
	// Give two Locations independent originals, including unknown and history-only content.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	one, two := locationTestSource(t, lib), locationTestSource(t, lib)
	files := []*File{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "unknown"}, {Name: "history"}, {Name: "unique"},
		{Name: "changed"}, {Name: "directory", Kind: entity.FileKind_FILE_KIND_DIRECTORY}}
	createFileRows(t, db, files...)
	signature := []byte{0, 255, 72}
	for index, location := range []*Location{one, two, one, two} {
		value := signature
		if index == 3 {
			value = nil
		}
		require.NoError(t, db.Create(&FileLocation{FileID: files[index].ID, LocationID: location.ID, Path: files[index].Name, Signature: value}).Error)
	}
	require.NoError(t, db.Create(&FileLocation{FileID: files[5].ID, LocationID: two.ID, Path: "unique", Signature: []byte("different")}).Error)
	require.NoError(t, db.Create(&FileLocation{FileID: files[6].ID, LocationID: two.ID, Path: "changed", Signature: []byte("edited")}).Error)
	// The covered originals already retain their saved versions, as inventory publication records them.
	for _, file := range []*File{files[0], files[1], files[2], files[3], files[4], files[6]} {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: signature}).Error)
	}
	require.NoError(t, db.Create(&Position{MediaID: 1, Path: "saved", Signature: signature}).Error)
	require.NoError(t, db.Create(&Position{MediaID: 2, Path: "second", Signature: signature}).Error)
	require.NoError(t, db.Create(&Position{MediaID: 1, Path: "folder/", IsDir: true, Signature: signature}).Error)

	// Status facts see actual non-directory copies without substituting history for unknown originals.
	ids := make([]int64, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.ID)
	}
	facts, err := lib.ReadFileFacts(ctx, ids, true, true)
	require.NoError(t, err)
	require.True(t, facts[files[0].ID].CurrentCopies)
	require.NotNil(t, facts[files[0].ID].Original.Signature)
	require.True(t, facts[files[0].ID].HasVersions)
	require.True(t, facts[files[3].ID].HasVersions)
	require.Nil(t, facts[files[3].ID].Original.Signature)
	require.False(t, facts[files[3].ID].CurrentCopies)
	require.Nil(t, facts[files[4].ID].Original)
	require.True(t, facts[files[4].ID].HasVersions)
	require.True(t, facts[files[6].ID].HasVersions)
	require.False(t, facts[files[6].ID].CurrentCopies)

	// Every original in a duplicate group appears exactly once across bounded search pages.
	var found []int64
	cursor := ""
	for {
		page, err := lib.ListFileRows(ctx, 0, entity.FileScope_FILE_SCOPE_ALL, true, "has:duplicates", cursor, 1)
		require.NoError(t, err)
		for _, result := range page.Files {
			found = append(found, result.ID)
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	require.Equal(t, []int64{files[0].ID, files[1].ID, files[2].ID}, found)
	page, err := lib.ListFileRows(ctx, 0, entity.FileScope_FILE_SCOPE_ALL, true, fmt.Sprintf("location:%d AND has:duplicates", two.ID), "", 10)
	require.NoError(t, err)
	require.Len(t, page.Files, 1, "Location filters narrow results, not the global comparison")
	require.Equal(t, files[1].ID, page.Files[0].ID)
	page, err = lib.ListFileRows(ctx, 0, entity.FileScope_FILE_SCOPE_ALL, true, "name:a AND has:duplicates", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Files, 1)

	// Removing one index does not merge Files; the two remaining originals still match within one Location.
	require.NoError(t, db.Delete(&FileLocation{}, "file_id = ?", files[1].ID).Error)
	page, err = lib.ListFileRows(ctx, 0, entity.FileScope_FILE_SCOPE_ALL, true, "has:duplicates", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Files, 2)
	require.NoError(t, db.Delete(&FileLocation{}, "file_id = ?", files[2].ID).Error)
	page, err = lib.ListFileRows(ctx, 0, entity.FileScope_FILE_SCOPE_ALL, true, "has:duplicates", "", 10)
	require.NoError(t, err)
	require.Empty(t, page.Files, "equal historical signatures alone do not make online duplicates")
}

func TestRestorableStatusMatchesPreparation(t *testing.T) {
	for _, test := range []struct {
		name       string
		health     entity.PositionHealth
		conflict   bool
		noMedia    bool
		noBaseline bool
		want       bool
	}{
		{name: "unchecked", want: true},
		{name: "old successful check", health: entity.PositionHealth_POSITION_HEALTH_HEALTHY, want: true},
		{name: "damaged", health: entity.PositionHealth_POSITION_HEALTH_DAMAGED},
		{name: "missing", health: entity.PositionHealth_POSITION_HEALTH_MISSING},
		{name: "unreadable", health: entity.PositionHealth_POSITION_HEALTH_UNREADABLE},
		{name: "conflicting eligible copy", conflict: true},
		{name: "unregistered media", noMedia: true},
		{name: "incomplete baseline", noBaseline: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db, lib := newTestLibrary(t)
			file := &File{Name: "document"}
			createFileRows(t, db, file)
			location := locationTestSource(t, lib)
			hash := sha256.Sum256([]byte("content"))
			version := &FileVersion{FileID: file.ID, Signature: []byte("opaque"), Hash: hash[:], Size: 7}
			if test.noBaseline {
				version.Hash = nil
			}
			require.NoError(t, db.Create(version).Error)
			require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: "document", Signature: version.Signature, Hash: version.Hash, Size: version.Size}).Error)
			if !test.noMedia {
				_, err := lib.CreateMedia(ctx, &Media{ID: 1, Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
					Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
				require.NoError(t, err)
			}
			require.NoError(t, db.Create(&Position{MediaID: 1, Path: "saved", Signature: version.Signature, Hash: hash[:],
				Size: 7, Health: test.health, CheckedAtNS: 1}).Error)
			if test.conflict {
				require.NoError(t, db.Create(&Position{MediaID: 1, Path: "conflicting", Signature: version.Signature, Hash: hash[:], Size: 8}).Error)
			}
			facts, err := lib.ReadFileFacts(ctx, []int64{file.ID}, false, true)
			require.NoError(t, err)
			status := facts[file.ID]
			require.Equal(t, test.want, status.CurrentArchive)
			require.Equal(t, test.want, status.HasArchive)
			require.Equal(t, !test.want, status.LatestUnavailable)
			selection, err := lib.InspectSelection(ctx, &entity.SelectionInspection{Restore: true, FileVersionIds: []int64{version.ID}})
			require.NoError(t, err)
			require.Equal(t, !test.want, selection.MissingCopyCount > 0)
		})
	}
}

func TestStatusRetainsOlderRestorableVersion(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	one, two := &File{Name: "one"}, &File{Name: "two"}
	createFileRows(t, db, one, two)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	hash := sha256.Sum256([]byte("content"))
	old, latest := int64(1), int64(2)
	for _, file := range []*File{one, two} {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("shared"), Hash: hash[:], Size: 7, LastArchivedAtNS: &old}).Error)
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("lost"), Hash: hash[:], Size: 7, LastArchivedAtNS: &latest}).Error)
	}
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "shared", Signature: []byte("shared"), Hash: hash[:], Size: 7}).Error)
	facts, err := lib.ReadFileFacts(ctx, []int64{one.ID, two.ID}, false, true)
	require.NoError(t, err)
	for _, file := range []*File{one, two} {
		require.Nil(t, facts[file.ID].Original)
		require.True(t, facts[file.ID].HasArchive, "a shared physical copy serves each File")
		require.True(t, facts[file.ID].LatestUnavailable)
	}
}

func TestFileStatusFactsCrossBatches(t *testing.T) {
	// Enough identities force multiple fixed-size reads without individual state calls.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	files := make([]*File, batchSize+3)
	for index := range files {
		files[index] = &File{Name: fmt.Sprintf("file-%04d", index)}
	}
	createFileRows(t, db, files...)
	ids := make([]int64, 0, len(files))
	for _, file := range files {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte(file.Name)}).Error)
		ids = append(ids, file.ID)
	}
	facts, err := lib.ReadFileFacts(ctx, ids, false, true)
	require.NoError(t, err)
	for _, file := range files {
		require.True(t, facts[file.ID].HasVersions)
	}
}

func TestFilesStatusMatrix(t *testing.T) {
	// Independent original, coverage and history facts determine the compact display projection.
	known := &FileLocation{Signature: []byte("known")}
	unknown := &FileLocation{}
	tests := []struct {
		name     string
		facts    FileReadFacts
		original entity.OriginalAvailability
		valid    bool
		archive  entity.FilesArchive
		coverage entity.FilesCoverage
		issues   []entity.FilesIssue
	}{
		{"covered", FileReadFacts{Original: known, CurrentArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_COVERED, nil},
		{"local only", FileReadFacts{Original: known}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_NONE, entity.FilesCoverage_FILES_COVERAGE_UNCOVERED, nil},
		{"changed with history", FileReadFacts{Original: known, HasArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_UNCOVERED, nil},
		{"unknown with history", FileReadFacts{Original: unknown, HasArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED, nil},
		{"unknown without history", FileReadFacts{Original: unknown}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_UNSPECIFIED, entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED, nil},
		{"missing saved", FileReadFacts{Original: known, HasArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, false, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_NOT_APPLICABLE, nil},
		{"unlinked saved", FileReadFacts{HasArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNLINKED, false, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_NOT_APPLICABLE, nil},
		{"missing latest", FileReadFacts{Original: known, HasArchive: true, LatestUnavailable: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, false, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_NOT_APPLICABLE, []entity.FilesIssue{entity.FilesIssue_FILES_ISSUE_LATEST_VERSION_UNAVAILABLE}},
		{"missing all copies", FileReadFacts{Original: known, HasVersions: true, LatestUnavailable: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, false, entity.FilesArchive_FILES_ARCHIVE_NONE, entity.FilesCoverage_FILES_COVERAGE_NOT_APPLICABLE, []entity.FilesIssue{entity.FilesIssue_FILES_ISSUE_ALL_COPIES_UNAVAILABLE, entity.FilesIssue_FILES_ISSUE_LATEST_VERSION_UNAVAILABLE}},
		{"missing no history", FileReadFacts{Original: known}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_MISSING, false, entity.FilesArchive_FILES_ARCHIVE_NONE, entity.FilesCoverage_FILES_COVERAGE_NOT_APPLICABLE, nil},
		{"local bad backups", FileReadFacts{Original: known, HasVersions: true, HasBadCopies: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_NONE, entity.FilesCoverage_FILES_COVERAGE_UNCOVERED, []entity.FilesIssue{entity.FilesIssue_FILES_ISSUE_ALL_COPIES_UNAVAILABLE}},
		{"unavailable saved", FileReadFacts{Original: known, HasArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_UNAVAILABLE, false, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED, nil},
		{"partial bad", FileReadFacts{Original: known, HasArchive: true, HasBadCopies: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, true, entity.FilesArchive_FILES_ARCHIVE_AVAILABLE, entity.FilesCoverage_FILES_COVERAGE_UNCOVERED, []entity.FilesIssue{entity.FilesIssue_FILES_ISSUE_PARTIAL_COPIES_UNAVAILABLE}},
		{"invalid observation does not claim coverage", FileReadFacts{Original: known, CurrentArchive: true}, entity.OriginalAvailability_ORIGINAL_AVAILABILITY_PRESENT, false, entity.FilesArchive_FILES_ARCHIVE_UNSPECIFIED, entity.FilesCoverage_FILES_COVERAGE_UNSPECIFIED, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// A valid observation knows the current content only when its original records a signature.
			actual := test.facts.Status(test.original, test.valid && len(test.facts.Original.Signature) > 0)
			require.Equal(t, test.original, actual.Original)
			require.Equal(t, test.archive, actual.Archive)
			require.Equal(t, test.coverage, actual.Current)
			require.Equal(t, test.issues, actual.Issues)
		})
	}
}
