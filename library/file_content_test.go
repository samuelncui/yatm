package library

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestFileContentSummariesAndOriginalDuplicates(t *testing.T) {
	// Give two Locations independent originals, including unknown and history-only content.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	one, two := onlineTestSource(t, lib), onlineTestSource(t, lib)
	files := []*File{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "unknown"}, {Name: "history"}, {Name: "unique"},
		{Name: "changed"}, {Name: "directory", Kind: entity.FileKind_FILE_KIND_DIRECTORY}}
	require.NoError(t, db.Create(files).Error)
	signature := []byte{0, 255, 72}
	for index, location := range []*Location{one, two, one, two} {
		value := signature
		if index == 3 {
			value = nil
		}
		require.NoError(t, db.Create(&FileLocation{FileID: files[index].ID, LocationID: location.ID, Path: files[index].Name, Signature: value, ObservedBindingToken: location.BindingToken}).Error)
	}
	require.NoError(t, db.Create(&FileLocation{FileID: files[5].ID, LocationID: two.ID, Path: "unique", Signature: []byte("different")}).Error)
	require.NoError(t, db.Create(&FileLocation{FileID: files[6].ID, LocationID: two.ID, Path: "changed", Signature: []byte("edited")}).Error)
	for _, file := range []*File{files[3], files[4], files[6]} {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: signature}).Error)
	}
	require.NoError(t, lib.SavePosition(ctx, &Position{MediaID: 1, Path: "saved", Signature: signature}))
	require.NoError(t, lib.SavePosition(ctx, &Position{MediaID: 2, Path: "second", Signature: signature}))
	require.NoError(t, lib.SavePosition(ctx, &Position{MediaID: 1, Path: "folder/", IsDir: true, Signature: signature}))

	// Summaries count actual non-directory copies without substituting history for unknown originals.
	require.NoError(t, lib.HydrateFileContent(ctx, append(files, nil, files[0])...))
	require.Equal(t, int64(2), files[0].ContentSummary.ArchivedCopies)
	require.True(t, files[0].ContentSummary.SignatureKnown)
	require.True(t, files[0].ContentSummary.HasVersions)
	require.True(t, files[3].ContentSummary.HasVersions)
	require.False(t, files[3].ContentSummary.SignatureKnown)
	require.Zero(t, files[3].ContentSummary.ArchivedCopies)
	require.False(t, files[4].ContentSummary.HasOriginal)
	require.True(t, files[4].ContentSummary.HasVersions)
	require.True(t, files[6].ContentSummary.HasVersions)
	require.Zero(t, files[6].ContentSummary.ArchivedCopies)
	require.Nil(t, files[7].ContentSummary)
	require.False(t, db.Migrator().HasColumn(ModelFile, "content_summary"))

	// Every original in a duplicate group appears exactly once across bounded search pages.
	var found []int64
	cursor := ""
	for {
		page, err := lib.SearchFiles(ctx, "has:duplicates", cursor, 1)
		require.NoError(t, err)
		for _, result := range page.Results {
			found = append(found, result.File.ID)
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	require.Equal(t, []int64{files[0].ID, files[1].ID, files[2].ID}, found)
	page, err := lib.SearchFiles(ctx, fmt.Sprintf("location:%d AND has:duplicates", two.ID), "", 10)
	require.NoError(t, err)
	require.Len(t, page.Results, 1, "Location filters narrow results, not the global comparison")
	require.Equal(t, files[1].ID, page.Results[0].File.ID)
	page, err = lib.SearchFiles(ctx, "name:a AND has:duplicates", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Results, 1)

	// Removing one index does not merge Files; the two remaining originals still match within one Location.
	require.NoError(t, db.Delete(&FileLocation{}, "file_id = ?", files[1].ID).Error)
	page, err = lib.SearchFiles(ctx, "has:duplicates", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Results, 2)
	require.NoError(t, db.Delete(&FileLocation{}, "file_id = ?", files[2].ID).Error)
	page, err = lib.SearchFiles(ctx, "has:duplicates", "", 10)
	require.NoError(t, err)
	require.Empty(t, page.Results, "equal historical signatures alone do not make online duplicates")
}

func TestRestorableSummaryMatchesPreparation(t *testing.T) {
	for _, test := range []struct {
		name       string
		health     entity.PositionHealth
		conflict   bool
		noMedia    bool
		noBaseline bool
		want       int64
	}{
		{name: "unchecked", want: 1},
		{name: "old successful check", health: entity.PositionHealth_HEALTHY, want: 1},
		{name: "damaged", health: entity.PositionHealth_DAMAGED},
		{name: "missing", health: entity.PositionHealth_MISSING},
		{name: "unreadable", health: entity.PositionHealth_UNREADABLE},
		{name: "conflicting eligible copy", conflict: true},
		{name: "unregistered media", noMedia: true},
		{name: "incomplete baseline", noBaseline: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db, lib := newTestLibrary(t)
			file := &File{Name: "document"}
			require.NoError(t, db.Create(file).Error)
			location := onlineTestSource(t, lib)
			hash := sha256.Sum256([]byte("content"))
			version := &FileVersion{FileID: file.ID, Signature: []byte("opaque"), Hash: hash[:], Size: 7}
			if test.noBaseline {
				version.Hash = nil
			}
			require.NoError(t, db.Create(version).Error)
			require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: "document",
				ObservedBindingToken: location.BindingToken, Signature: version.Signature, Hash: version.Hash, Size: version.Size}).Error)
			if !test.noMedia {
				_, err := lib.CreateMedia(ctx, &Media{ID: 1, Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
					Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
				require.NoError(t, err)
			}
			require.NoError(t, db.Create(&Position{MediaID: 1, Path: "saved", Signature: version.Signature, Hash: hash[:],
				Size: 7, Health: test.health, CheckedAt: 1}).Error)
			if test.conflict {
				require.NoError(t, db.Create(&Position{MediaID: 1, Path: "conflicting", Signature: version.Signature, Hash: hash[:], Size: 8}).Error)
			}
			require.NoError(t, lib.HydrateFileContent(ctx, file))
			summary := file.ContentSummary
			require.Equal(t, test.want, summary.RestorableCurrentCopies)
			require.Equal(t, test.want, summary.RestorableVersionCopies)
			require.Equal(t, test.want, summary.LatestVersionRestorableCopies)
			selection, err := lib.InspectSelection(ctx, &entity.InspectSelectionRequest{Restore: true, FileVersionIds: []int64{version.ID}})
			require.NoError(t, err)
			require.Equal(t, test.want == 0, selection.MissingCopies > 0)
		})
	}
}

func TestSummaryRetainsOlderRestorableVersion(t *testing.T) {
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	one, two := &File{Name: "one"}, &File{Name: "two"}
	require.NoError(t, db.Create([]*File{one, two}).Error)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	hash := sha256.Sum256([]byte("content"))
	old, latest := int64(1), int64(2)
	for _, file := range []*File{one, two} {
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("shared"), Hash: hash[:], Size: 7, LastArchivedAt: &old}).Error)
		require.NoError(t, db.Create(&FileVersion{FileID: file.ID, Signature: []byte("lost"), Hash: hash[:], Size: 7, LastArchivedAt: &latest}).Error)
	}
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "shared", Signature: []byte("shared"), Hash: hash[:], Size: 7}).Error)
	require.NoError(t, lib.HydrateFileContent(ctx, one, two))
	for _, file := range []*File{one, two} {
		require.Equal(t, entity.OriginalAvailability_ORIGINAL_UNLINKED, file.ContentSummary.OriginalAvailability)
		require.EqualValues(t, 1, file.ContentSummary.RestorableVersionCopies, "shared physical copies count once per File")
		require.Zero(t, file.ContentSummary.LatestVersionRestorableCopies)
	}
}

func TestFileContentSummariesCrossBatches(t *testing.T) {
	// Enough identities force multiple fixed-size reads without individual state calls.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	files := make([]*File, batchSize+3)
	for index := range files {
		files[index] = &File{Name: fmt.Sprintf("file-%04d", index)}
	}
	require.NoError(t, db.CreateInBatches(files, batchSize).Error)
	require.NoError(t, lib.HydrateFileContent(ctx, files...))
	for _, file := range files {
		require.NotNil(t, file.ContentSummary)
	}
}
