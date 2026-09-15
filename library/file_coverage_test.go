package library

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestSyncRetainsExistingCoverageAsOwnVersion(t *testing.T) {
	// Archive another independently organized File with an opaque signature and known dates.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	archived := &File{Name: "archive-name", Note: "other annotations"}
	require.NoError(t, lib.SaveFile(ctx, archived))
	signature := []byte{0, 255, 4, 80}
	observation := onlineTestPosition("handbook.md", "saved content")
	observation.Signature, observation.Mode = signature, 0600
	first, last := int64(100), int64(200)
	require.NoError(t, db.Create(&FileVersion{FileID: archived.ID, Signature: signature,
		Hash: observation.Hash, Size: observation.Size, Mode: 0644, MtimeNS: 9,
		FirstArchivedAt: &first, LastArchivedAt: &last}).Error)
	copy := &Position{MediaID: 1, Path: "other-name", Signature: signature, Hash: observation.Hash, Size: observation.Size}
	require.NoError(t, lib.SavePosition(ctx, copy))
	location := onlineTestSource(t, lib)

	// Sync creates one saved version, using this original's metadata without copying annotations.
	location, err := lib.PublishOnline(ctx, location.ID, location.Revision, 1, onlineTestManifest(observation))
	require.NoError(t, err)
	versions, more, err := lib.ListFileVersions(ctx, observation.FileID, 0, 10)
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, versions, 1)
	saved := versions[0]
	require.NotEqual(t, archived.ID, saved.FileID)
	require.Equal(t, signature, saved.Signature)
	require.Equal(t, observation.Mode, saved.Mode)
	require.Equal(t, observation.MtimeNS, saved.MtimeNS)
	require.Equal(t, &first, saved.FirstArchivedAt)
	require.Equal(t, &last, saved.LastArchivedAt)
	file, err := lib.GetFile(ctx, observation.FileID)
	require.NoError(t, err)
	require.Empty(t, file.Note)

	// Additional physical copies and repeated Sync never duplicate versions or invent later dates.
	require.NoError(t, lib.SavePosition(ctx, &Position{MediaID: 2, Path: "second", Signature: signature,
		Hash: observation.Hash, Size: observation.Size, WriteTime: time.Now()}))
	location, err = lib.PublishOnline(ctx, location.ID, location.Revision, 2, onlineTestManifest(observation))
	require.NoError(t, err)
	versions, _, err = lib.ListFileVersions(ctx, file.ID, 0, 10)
	require.NoError(t, err)
	require.Equal(t, []*FileVersion{saved}, versions)
	state, err := lib.FileState(ctx, file.ID)
	require.NoError(t, err)
	require.EqualValues(t, 2, state.ArchivedCopies)
	require.Equal(t, saved.ID, state.LatestVersion.Id)

	// Editing and later unlinking the original retain the saved state and its restore metadata.
	changed := onlineTestPosition("handbook.md", "new unsaved content")
	changed.FileID = file.ID
	location, err = lib.PublishOnline(ctx, location.ID, location.Revision, 3, onlineTestManifest(changed))
	require.NoError(t, err)
	require.NoError(t, lib.DeleteOnlineSource(ctx, location.ID, location.Revision))
	require.NoError(t, lib.DeletePositions(ctx, copy.ID))
	versions, _, err = lib.ListFileVersions(ctx, file.ID, 0, 10)
	require.NoError(t, err)
	require.Equal(t, []*FileVersion{saved}, versions)
}

func TestInventoryPublicationRetainsAlreadyIndexedContent(t *testing.T) {
	for _, operation := range []string{"archive", "scan", "position"} {
		t.Run(operation, func(t *testing.T) {
			// Two independent originals exist before any physical archive copy.
			ctx := context.Background()
			_, lib := newTestLibrary(t)
			location := onlineTestSource(t, lib)
			one, two := onlineTestPosition("one", "same"), onlineTestPosition("two", "same")
			_, err := lib.PublishOnline(ctx, location.ID, location.Revision, 1, onlineTestManifest(one, two))
			require.NoError(t, err)
			media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
				Identity: "11111111-1111-1111-1111-111111111111", Name: "Archive",
				Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
			require.NoError(t, err)

			// Each public inventory publication boundary establishes both Files' saved versions.
			switch operation {
			case "archive":
				_, err = lib.CommitMedia(ctx, media, func(_ context.Context, yield func(*MediaFile) error) error {
					return yield(&MediaFile{Path: "saved", Size: one.Size, Hash: one.Hash, Mode: 0644,
						Expected: &entity.ExpectedFile{FileId: one.FileID, Signature: one.Signature,
							Sha256: one.Hash, Size: one.Size, Mode: one.Mode, MtimeNs: one.MtimeNS}})
				})
			case "scan":
				_, err = lib.ApplyScan(ctx, media.ID, func(_ context.Context, yield func(*entity.ScanEntry) error) error {
					return yield(&entity.ScanEntry{Path: "saved", Change: entity.ScanChange_SCAN_CHANGE_ADDED,
						Sha256: one.Hash, Size: one.Size, Mode: 0644, MtimeNs: 123})
				})
			case "position":
				err = lib.SavePosition(ctx, &Position{MediaID: media.ID, Path: "saved", Signature: one.Signature,
					Hash: one.Hash, Size: one.Size})
			}
			require.NoError(t, err)

			// Copies are shared but File identity, restore facts and historical dates are not fabricated.
			for _, observation := range []*OnlinePosition{one, two} {
				versions, _, err := lib.ListFileVersions(ctx, observation.FileID, 0, 10)
				require.NoError(t, err)
				require.Len(t, versions, 1)
				require.Equal(t, observation.Signature, versions[0].Signature)
				require.Equal(t, observation.MtimeNS, versions[0].MtimeNS)
				if operation == "archive" {
					require.NotNil(t, versions[0].FirstArchivedAt)
					continue
				}
				require.Nil(t, versions[0].FirstArchivedAt)
				require.Nil(t, versions[0].LastArchivedAt)
			}
		})
	}
}

func TestCoveredVersionsReconcileExistingCatalogAndImport(t *testing.T) {
	// Existing indexed originals and copy inventory may precede the coverage rule.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	one := onlineTestPosition("one", "same")
	_, err := lib.PublishOnline(ctx, location.ID, location.Revision, 1, onlineTestManifest(one))
	require.NoError(t, err)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "11111111-1111-1111-1111-111111111111",
		Profile:  (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: "copy", Signature: one.Signature, Hash: one.Hash, Size: one.Size}).Error)

	// Reads do not mutate the catalog; startup reconciles it once before serving.
	versions, _, err := lib.ListFileVersions(ctx, one.FileID, 0, 10)
	require.NoError(t, err)
	require.Empty(t, versions)
	var backup bytes.Buffer
	require.NoError(t, lib.Export(ctx, &backup, []entity.LibraryEntityType{
		entity.LibraryEntityType_FILE, entity.LibraryEntityType_MEDIA, entity.LibraryEntityType_POSITION,
		entity.LibraryEntityType_LOCATION, entity.LibraryEntityType_FILE_LOCATION,
	}))
	require.NoError(t, lib.AutoMigrate())
	versions, _, err = lib.ListFileVersions(ctx, one.FileID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.NoError(t, lib.AutoMigrate())
	again, _, err := lib.ListFileVersions(ctx, one.FileID, 0, 10)
	require.NoError(t, err)
	require.Equal(t, versions, again)

	// Import handles complete entity groups before deriving versions, regardless of root confirmation.
	_, imported := newTestLibrary(t)
	require.NoError(t, imported.Import(ctx, bytes.NewReader(backup.Bytes())))
	versions, _, err = imported.ListFileVersions(ctx, one.FileID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	root, err := imported.GetOnlineSource(ctx, location.ID)
	require.NoError(t, err)
	require.Equal(t, entity.OnlineBinding_UNCONFIRMED, root.Binding)

	// A late content contradiction rolls back replacement and any derived version associations.
	broken := strings.Replace(backup.String(), `"size":4`, `"size":999`, 1)
	require.ErrorContains(t, imported.Import(ctx, strings.NewReader(broken)), "size disagrees")
	kept, _, err := imported.ListFileVersions(ctx, one.FileID, 0, 10)
	require.NoError(t, err)
	require.Equal(t, versions, kept)
}

func TestCoveredVersionsBoundedPublicationAndRollback(t *testing.T) {
	// A final contradictory original must roll back versions already created in earlier pages.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	signature := []byte{255, 0, 3}
	require.NoError(t, lib.SavePosition(ctx, &Position{MediaID: 1, Path: "saved", Signature: signature, Size: 9}))
	rows := make([]*OnlinePosition, batchSize+3)
	for index := range rows {
		rows[index] = &OnlinePosition{Path: fmt.Sprintf("%04d", index), Signature: signature, Size: 9, Mode: 0600}
	}
	rows[len(rows)-1].Size = 10
	_, err := lib.PublishOnline(ctx, location.ID, location.Revision, 1, onlineTestManifest(rows...))
	require.ErrorContains(t, err, "size disagrees")
	var count int64
	require.NoError(t, db.Model(&FileVersion{}).Count(&count).Error)
	require.Zero(t, count)

	// Retrying a complete valid manifest creates every page once without merging equal content.
	for _, row := range rows {
		row.FileID, row.Size = 0, 9
	}
	_, err = lib.PublishOnline(ctx, location.ID, location.Revision, 2, onlineTestManifest(rows...))
	require.NoError(t, err)
	require.NoError(t, db.Model(&FileVersion{}).Count(&count).Error)
	require.EqualValues(t, len(rows), count)
}

func TestCoveredVersionsRequireKnownConsistentContent(t *testing.T) {
	for _, scenario := range []string{"unsigned", "directory", "hash conflict"} {
		t.Run(scenario, func(t *testing.T) {
			// Only a non-directory copy of known, noncontradictory content can establish a version.
			ctx := context.Background()
			db, lib := newTestLibrary(t)
			location := onlineTestSource(t, lib)
			original := onlineTestPosition("original", "content")
			original.Signature = []byte{0, 255, 100}
			copy := &Position{MediaID: 1, Path: "copy", Signature: original.Signature, Hash: original.Hash, Size: original.Size}
			switch scenario {
			case "unsigned":
				original.Signature, original.Hash = nil, nil
			case "directory":
				copy.IsDir = true
			case "hash conflict":
				copy.Hash = bytes.Repeat([]byte{7}, 32)
			}
			require.NoError(t, lib.SavePosition(ctx, copy))

			// Invalid or absent evidence cannot create even a transient saved-version record.
			_, err := lib.PublishOnline(ctx, location.ID, location.Revision, 1, onlineTestManifest(original))
			if scenario == "hash conflict" {
				require.ErrorContains(t, err, "hash disagrees")
			} else {
				require.NoError(t, err)
			}
			var count int64
			require.NoError(t, db.Model(&FileVersion{}).Count(&count).Error)
			require.Zero(t, count)
		})
	}
}

func TestCoveredVersionReadsAllCopyPages(t *testing.T) {
	// A known opaque signature may have many physical copies but only one version per File.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	location := onlineTestSource(t, lib)
	observation := onlineTestPosition("original", "content")
	observation.Signature, observation.Hash = []byte{0, 255, 7}, nil
	for index := 0; index < batchSize+1; index++ {
		copy := &Position{MediaID: 1, Path: fmt.Sprintf("copy-%04d", index), Signature: observation.Signature, Size: observation.Size}
		if index == batchSize {
			copy.Hash = onlineTestPosition("", "content").Hash
		}
		require.NoError(t, db.Create(copy).Error)
	}

	// The last page supplies content facts without rereading physical files or mutating the observation.
	_, err := lib.PublishOnline(ctx, location.ID, location.Revision, 1, onlineTestManifest(observation))
	require.NoError(t, err)
	versions, _, err := lib.ListFileVersions(ctx, observation.FileID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, onlineTestPosition("", "content").Hash, versions[0].Hash)
	original, err := lib.GetFileLocation(ctx, observation.FileID)
	require.NoError(t, err)
	require.Empty(t, original.Hash)
}
