package library

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func restorePublicationFixture(t *testing.T) (*gorm.DB, *Library, *Location, *File, *RestorePublication) {
	t.Helper()
	db, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	file := &File{Name: "photo.jpg", Note: "Keep source annotations", Kind: entity.FileKind_FILE_KIND_REGULAR}
	require.NoError(t, db.Create(file).Error)
	require.NoError(t, db.Create(&FileTag{FileID: file.ID, Tag: "important"}).Error)
	hash := sha256.Sum256([]byte("saved"))
	date := int64(1234)
	version := &FileVersion{FileID: file.ID, Signature: []byte{0xff, 0, 0x42}, Hash: hash[:], Size: 5, Mode: 0640, MtimeNS: 2000000001,
		FirstArchivedAt: &date, LastArchivedAt: &date}
	require.NoError(t, db.Create(version).Error)
	publication := &RestorePublication{Reconnect: true, ParentID: file.ParentID, Name: file.Name,
		Result: RestoreResult{OperationID: uuid.NewString(), ItemID: version.ID, SourceFileID: file.ID, SourceVersionID: version.ID,
			LocationID: location.ID, BindingToken: location.BindingToken, Path: "recovered/photo.jpg", Signature: version.Signature,
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
	current, err := lib.GetOnlineSource(ctx, location.ID)
	require.NoError(t, err)
	require.True(t, original.CurrentBinding(current))
	require.Equal(t, entity.OnlineBinding_CONFIRMED, current.Binding)
	require.Zero(t, current.LastSyncAt)
	require.Greater(t, current.Revision, location.Revision)
	require.Equal(t, "recovered/", original.ParentPath)
	require.False(t, db.Migrator().HasTable("location_directories"))

	// A successful operation remains idempotent after its original has been moved by a later scan.
	original.Path = "elsewhere/photo.jpg"
	require.NoError(t, db.Save(original).Error)
	repeated, err := lib.PublishRestore(ctx, publication)
	require.NoError(t, err)
	require.Equal(t, result, repeated)
	var count int64
	require.NoError(t, db.Model(&RestoreResult{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
	require.NoError(t, db.Model(&File{}).Count(&count).Error)
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
	require.EqualValues(t, 1234, *versions[0].FirstArchivedAt)
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
				location.Exclusions = &entity.OnlineExclusions{Format: "gitignore", Text: "recovered/\n!recovered/photo.jpg\n"}
				updated, err := lib.UpdateOnlineSource(ctx, location, false)
				require.NoError(t, err)
				publication.Result.BindingToken = updated.BindingToken
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
			require.NoError(t, db.Model(&File{}).Count(&count).Error)
			require.EqualValues(t, 1, count)
		})
	}
}

func TestRestorePublicationRejectsStaleBindingAndOwnedOutputAtomically(t *testing.T) {
	ctx := context.Background()
	db, lib, location, file, publication := restorePublicationFixture(t)
	publication.Result.BindingToken = uuid.NewString()
	_, err := lib.PublishRestore(ctx, publication)
	require.ErrorIs(t, err, ErrOnlineConflict)
	publication.Result.BindingToken = location.BindingToken
	require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID, Path: publication.Result.Path, Mode: 0644}).Error)
	_, err = lib.PublishRestore(ctx, publication)
	require.ErrorContains(t, err, "already linked")
	var count int64
	require.NoError(t, db.Model(&RestoreResult{}).Count(&count).Error)
	require.Zero(t, count)
	require.NoError(t, db.Model(&File{}).Count(&count).Error)
	require.EqualValues(t, 1, count)
}

func TestImportedRestoreResultCannotAuthorizeRetryAndLegacyReplacementClearsIt(t *testing.T) {
	ctx := context.Background()
	db, lib, _, _, publication := restorePublicationFixture(t)
	result, err := lib.PublishRestore(ctx, publication)
	require.NoError(t, err)
	require.NoError(t, db.Model(&RestoreResult{}).Where("operation_id = ?", result.OperationID).Update("binding_token", "").Error)
	_, err = lib.GetRestoreResult(ctx, result.OperationID, result.ItemID)
	require.ErrorContains(t, err, "imported")
	_, err = lib.PublishRestore(ctx, publication)
	require.ErrorContains(t, err, "imported")
	require.NoError(t, invalidateOnlineImport(db))
	var count int64
	require.NoError(t, db.Model(&RestoreResult{}).Count(&count).Error)
	require.Zero(t, count)
}

func TestLocationBindingTokenChangesOnlyForAccessBindingChanges(t *testing.T) {
	ctx := context.Background()
	_, lib, location, _, publication := restorePublicationFixture(t)
	_, err := lib.PublishRestore(ctx, publication)
	require.NoError(t, err)
	location, err = lib.GetOnlineSource(ctx, location.ID)
	require.NoError(t, err)
	token := location.BindingToken
	location.Name, location.RestoreTarget = "Renamed location", !location.RestoreTarget
	location, err = lib.UpdateOnlineSource(ctx, location, false)
	require.NoError(t, err)
	require.Equal(t, token, location.BindingToken, "display/recommendation changes must not disable valid files")
	location.Exclusions = &entity.OnlineExclusions{Format: "gitignore", Text: "*.tmp\n"}
	location, err = lib.UpdateOnlineSource(ctx, location, false)
	require.NoError(t, err)
	require.NotEqual(t, token, location.BindingToken)
	original, err := lib.GetFileLocation(ctx, publication.Result.SourceFileID)
	require.NoError(t, err)
	require.False(t, original.CurrentBinding(location))
	require.Equal(t, token, original.ObservedBindingToken, "stale observations stay available for browsing")
}
