package library

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestFileVersionOwnsArchivedFactsWithoutCopyOwnership(t *testing.T) {
	// Establish independent organization with no persisted File-level content columns.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	for _, column := range []string{"signature", "hash", "size", "mode", "mod_time"} {
		require.False(t, db.Migrator().HasColumn(ModelFile, column), column)
	}
	require.False(t, db.Migrator().HasColumn(ModelPosition, "file_id"))
	one, two := &File{Name: "one"}, &File{Name: "two"}
	require.NoError(t, lib.SaveFile(ctx, one))
	require.NoError(t, lib.SaveFile(ctx, two))
	signature := []byte{0, 255, 72}
	first, later := int64(100), int64(200)
	var version *FileVersion
	// Record first and repeated archival without changing the version's own restore metadata.
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		var err error
		version, err = recordVersion(tx, &FileVersion{FileID: one.ID, Signature: signature, Size: 9, Hash: []byte("opaque facts"), Mode: 0600, MtimeNS: 7, FirstArchivedAt: &first, LastArchivedAt: &first})
		return err
	}))
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		same, err := recordVersion(tx, &FileVersion{FileID: one.ID, Signature: signature, Size: 9, Hash: []byte("opaque facts"), Mode: 0644, MtimeNS: 8, FirstArchivedAt: &later, LastArchivedAt: &later})
		require.NoError(t, err)
		require.Equal(t, version.ID, same.ID)
		require.Equal(t, &first, same.FirstArchivedAt)
		require.Equal(t, &later, same.LastArchivedAt)
		require.EqualValues(t, 0600, same.Mode)
		_, err = recordVersion(tx, &FileVersion{FileID: two.ID, Signature: signature, Size: 9, Hash: []byte("opaque facts"), Mode: 0640})
		return err
	}))
	// A shared copy serves both histories without either version owning it.
	copy := &Position{MediaID: 1, Path: "physical", Signature: signature, Size: 9}
	require.NoError(t, lib.SavePosition(ctx, copy))
	copies, err := lib.MGetPositionByFileID(ctx, one.ID, two.ID)
	require.NoError(t, err)
	require.Len(t, copies[one.ID], 1)
	require.Len(t, copies[two.ID], 1)
	require.Equal(t, copy.ID, copies[one.ID][0].ID)
	require.NoError(t, lib.DeletePositions(ctx, copy.ID))
	kept, err := lib.GetFileVersion(ctx, version.ID)
	require.NoError(t, err)
	require.Equal(t, signature, kept.Signature)
	require.NoError(t, lib.Trim(ctx, true, true))
	_, err = lib.GetFile(ctx, one.ID)
	require.NoError(t, err, "ordinary Trim must not delete version history")
}

func TestOriginalIdentityIsExplicitAndNullable(t *testing.T) {
	// Unsigned originals still create two independent ongoing Files.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	location, err := lib.PublishOnline(ctx, location.ID, location.Revision, 1, onlineTestManifest(
		&OnlinePosition{Path: "a", Mode: 0644}, &OnlinePosition{Path: "b", Mode: 0644}))
	require.NoError(t, err)
	rows, err := lib.OnlineFilesPage(ctx, location.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, rows, 2)
	require.NotEqual(t, rows[0].FileID, rows[1].FileID)
	require.Nil(t, rows[0].Signature)
	// New content replaces the observation, not organization or saved history.
	file, err := lib.GetFile(ctx, rows[0].FileID)
	require.NoError(t, err)
	file.Note = "stable organization"
	require.NoError(t, lib.SaveFile(ctx, file))
	changed := onlineTestPosition("a", "new content")
	changed.FileID = file.ID
	_, err = lib.PublishOnline(ctx, location.ID, location.Revision, 2, onlineTestManifest(changed))
	require.NoError(t, err)
	kept, err := lib.GetFile(ctx, file.ID)
	require.NoError(t, err)
	require.Equal(t, file.Note, kept.Note)
	versions, _, err := lib.ListFileVersions(ctx, file.ID, 0, 10)
	require.NoError(t, err)
	require.Empty(t, versions)
}

func TestReturningToSavedContentReusesItsVersion(t *testing.T) {
	// Archive C1, then C2, then C1 again without turning a saved-content set into an event log.
	db, lib := newTestLibrary(t)
	file := &File{Name: "draft", Note: "ongoing organization"}
	require.NoError(t, lib.SaveFile(context.Background(), file))
	ids := make([]int64, 0, 3)
	for index, signature := range []string{"C1", "C2", "C1"} {
		when := int64(100 + index)
		require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
			version, err := recordVersion(tx, &FileVersion{FileID: file.ID, Signature: []byte(signature),
				Hash: []byte(signature), Size: 2, Mode: 0600, FirstArchivedAt: &when, LastArchivedAt: &when})
			if err != nil {
				return err
			}
			ids = append(ids, version.ID)
			return nil
		}))
	}

	// Returning content updates only its latest archive time; both content states remain available.
	require.Equal(t, ids[0], ids[2])
	require.NotEqual(t, ids[0], ids[1])
	versions, more, err := lib.ListFileVersions(context.Background(), file.ID, 0, 10)
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, versions, 2)
	require.EqualValues(t, 100, *versions[0].FirstArchivedAt)
	require.EqualValues(t, 102, *versions[0].LastArchivedAt)
	kept, err := lib.GetFile(context.Background(), file.ID)
	require.NoError(t, err)
	require.Equal(t, file.Note, kept.Note)
}
