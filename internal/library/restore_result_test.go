package library

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func restorePublicationFixture(t *testing.T) (*gorm.DB, *Library, *Location, *File, *RestorePublication) {
	t.Helper()
	db, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	file := &File{Name: "photo.jpg", Note: "Keep source annotations", Kind: entity.FileKind_FILE_KIND_REGULAR}
	createFileRows(t, db, file)
	require.NoError(t, db.Create(&FileTag{FileID: file.ID, Tag: "important"}).Error)
	hash := sha256.Sum256([]byte("saved"))
	date := int64(1234)
	version := &FileVersion{FileID: file.ID, Signature: []byte{0xff, 0, 0x42}, Hash: hash[:], Size: 5, Mode: 0640, MtimeNS: 2000000001,
		FirstArchivedAtNS: &date, LastArchivedAtNS: &date}
	require.NoError(t, db.Create(version).Error)
	publication := &RestorePublication{Reconnect: true, ParentID: file.ParentID, Name: file.Name,
		Result: RestoreResult{SourceFileID: file.ID, SourceVersionID: version.ID,
			LocationID: location.ID, Path: "recovered/photo.jpg", Signature: version.Signature,
			ExpectedHash: hash[:], ExpectedSize: 5, ActualHash: hash[:], ActualSize: 5},
		Original: &FileLocation{Size: 5, Mode: 0640, MtimeNS: version.MtimeNS, Hash: hash[:]}}
	return db, lib, location, file, publication
}

func TestRestorePublicationReconnectsOneObservationWithoutClaimingFullScan(t *testing.T) {
	ctx := context.Background()
	db, lib, location, file, publication := restorePublicationFixture(t)
	result, err := lib.PublishRestore(ctx, publication)
	require.NoError(t, err)
	require.Equal(t, RestoreReconnected, result.Outcome)
	require.Equal(t, file.ID, result.ResultFileID)
	original, err := lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	current, err := lib.GetLocation(ctx, location.ID)
	require.NoError(t, err)
	require.Equal(t, location.ID, original.LocationID)
	require.Zero(t, current.LastSyncAtNS)
	require.Greater(t, current.Revision, location.Revision)
	require.Equal(t, "recovered/", original.ParentPath)
	require.False(t, db.Migrator().HasTable("location_directories"))

	// Repeating a publication reports the actual occupied target, without a receipt table.
	_, err = lib.PublishRestore(ctx, publication)
	require.ErrorContains(t, err, "already linked")
	require.False(t, db.Migrator().HasTable("restore_results"))
	var count int64
	require.NoError(t, db.Model(ModelFile).Count(&count).Error)
	require.EqualValues(t, 1, count)

}

func TestRestorePublicationExistingBindingCreatesIndependentFile(t *testing.T) {
	ctx := context.Background()
	db, lib, location, file, publication := restorePublicationFixture(t)
	// Even an old, unverified observation prevents replacing the File's existing original.
	require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: "old/photo.jpg", Size: 9, Mode: 0644}).Error)
	result, err := lib.PublishRestore(ctx, publication)
	require.NoError(t, err)
	require.Equal(t, RestoreNewFile, result.Outcome)
	require.NotEqual(t, file.ID, result.ResultFileID)
	created, err := lib.GetFile(ctx, result.ResultFileID)
	require.NoError(t, err)
	require.Equal(t, RestoredName(file.Name, publication.Result.SourceVersionID), created.Name)
	require.Empty(t, created.Note)
	var tags []FileTag
	require.NoError(t, db.Where("file_id = ?", created.ID).Find(&tags).Error)
	require.Empty(t, tags)
	versions, _, err := lib.ListFileVersions(ctx, created.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, publication.Result.Signature, versions[0].Signature)
	require.EqualValues(t, 1234, *versions[0].FirstArchivedAtNS)
	previous, err := lib.GetFileLocation(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, "old/photo.jpg", previous.Path)
	untouched, err := lib.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, file.Note, untouched.Note)
}

func TestRestorePublicationKeepsIgnoredAndDamagedOutputsUnlinked(t *testing.T) {
	for _, test := range []struct {
		name             string
		ignored, damaged bool
		outcome          RestoreOutcome
	}{
		{"ignored ancestor", true, false, RestoreIgnored},
		{"complete damaged bytes", false, true, RestoreDamaged},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			db, lib, location, file, publication := restorePublicationFixture(t)
			if test.ignored {
				location.Config.Ignore = &entity.IgnoreRules{Format: "gitignore", Text: "recovered/\n!recovered/photo.jpg\n"}
				_, err := lib.UpdateLocation(ctx, location)
				require.NoError(t, err)
			}
			if test.damaged {
				hash := sha256.Sum256([]byte("damaged"))
				publication.Result.ActualHash, publication.Result.ActualSize = hash[:], 7
				publication.Result.Outcome = RestoreDamaged
			}
			publication.Original = nil // These outcomes require no executable file observation.
			result, err := lib.PublishRestore(ctx, publication)
			require.NoError(t, err)
			require.Equal(t, test.outcome, result.Outcome)
			require.Zero(t, result.ResultFileID)
			original, err := lib.GetFileLocation(ctx, file.ID)
			require.NoError(t, err)
			require.Nil(t, original)
			var count int64
			require.NoError(t, db.Model(ModelFile).Count(&count).Error)
			require.EqualValues(t, 1, count)
		})
	}
}

func TestRestorePublicationRejectsOwnedOutputAtomically(t *testing.T) {
	ctx := context.Background()
	db, lib, location, file, publication := restorePublicationFixture(t)
	require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: publication.Result.Path, Mode: 0644}).Error)
	_, err := lib.PublishRestore(ctx, publication)
	require.ErrorContains(t, err, "already linked")
	var count int64
	require.NoError(t, db.Model(ModelFile).Count(&count).Error)
	require.EqualValues(t, 1, count)
}
