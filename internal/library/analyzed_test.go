package library

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestPublishAnalyzedPartialRangeRetainsCompleteScanMarkers(t *testing.T) {
	// Establish complete-scan history and originals in selected and unselected ranges.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	complete, err := lib.PublishAnalyzed(ctx, location.ID, 11, observedTestManifest(
		observedTestEntry("outside.txt", "untouched"),
		observedTestEntry("selected/kept.txt", "old"),
		observedTestEntry("selected/removed.txt", "removed")), nil)
	require.NoError(t, err)
	before, err := lib.LocationOriginalsPage(ctx, location.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, before, 3)

	// A selected range can publish new facts and absence without a whole-Location Job marker.
	partial, err := lib.PublishAnalyzed(ctx, location.ID, 0, observedTestManifest(&ObservedEntry{
		FileID: before[1].FileID, Path: before[1].Path, Size: 19, Mode: 0644, MtimeNS: 2000000001,
	}), observedTestManifest(&ObservedEntry{FileID: before[2].FileID, Path: before[2].Path}))
	require.NoError(t, err)
	after, err := lib.LocationOriginalsPage(ctx, location.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, after, 2)
	require.Equal(t, before[0], after[0])
	require.Equal(t, before[1].FileID, after[1].FileID)
	require.EqualValues(t, 19, after[1].Size)
	require.Empty(t, after[1].Hash)
	require.Empty(t, after[1].Signature)
	_, err = lib.GetFile(ctx, before[2].FileID)
	require.NoError(t, err, "absence detaches the original without removing its logical File")
	require.Equal(t, complete.LastSyncJobID, partial.LastSyncJobID)
	require.Equal(t, complete.LastSyncAtNS, partial.LastSyncAtNS)
	stored, err := lib.GetLocation(ctx, location.ID)
	require.NoError(t, err)
	require.Equal(t, complete.LastSyncJobID, stored.LastSyncJobID)
	require.Equal(t, complete.LastSyncAtNS, stored.LastSyncAtNS)
}

func TestPublishAnalyzedReconcilesOnlyObservedOriginals(t *testing.T) {
	// Seed two valid originals with known copies but no saved-version ownership yet.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	media := &Media{Name: "Copies", Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()}
	require.NoError(t, db.Create(media).Error)
	selected, outside := &File{Name: "selected"}, &File{Name: "outside"}
	createFileRows(t, db, selected, outside)
	for _, file := range []*File{selected, outside} {
		signature := []byte(file.Name)
		require.NoError(t, db.Create(&Position{MediaID: media.ID, Path: file.Name, Size: 4, Signature: signature}).Error)
		require.NoError(t, db.Create(&FileLocation{FileID: file.ID, LocationID: location.ID,
			Path: file.Name, Size: 4, Mode: 0644, MtimeNS: 123, Signature: signature}).Error)
	}

	// A partial observation must not scan or create ownership for unrelated originals.
	_, err := lib.PublishAnalyzed(ctx, location.ID, 0, observedTestManifest(&ObservedEntry{
		FileID: selected.ID, Path: selected.Name, Size: 4, Mode: 0644, MtimeNS: 123,
		Signature: []byte(selected.Name),
	}), nil)
	require.NoError(t, err)
	var versions []*FileVersion
	require.NoError(t, db.Order("file_id").Find(&versions).Error)
	require.Len(t, versions, 1)
	require.Equal(t, selected.ID, versions[0].FileID)
}
