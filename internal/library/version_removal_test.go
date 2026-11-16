package library

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestRemovingLastVersionLeavesPositionIndependent(t *testing.T) {
	// Removing catalog history never deletes the independent Position that can establish it again.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	location := locationTestSource(t, lib)
	observation := observedTestEntry("covered.txt", "known archived content")
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	_, err = lib.PublishAnalyzed(ctx, location.ID, 1, observedTestManifest(observation), nil)
	require.NoError(t, err)
	_, err = lib.CommitMedia(ctx, media, func(_ context.Context, yield func(*MediaFile) error) error {
		return yield(&MediaFile{Path: "covered-copy", Hash: observation.Hash, Size: observation.Size, Mode: 0644})
	})
	require.NoError(t, err)
	versions, _, err := lib.ListFileVersions(ctx, observation.FileID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	removed := versions[0]

	_, err = lib.RemoveFileVersion(ctx, observation.FileID, removed.ID, false)
	require.NoError(t, err)
	versions, _, err = lib.ListFileVersions(ctx, observation.FileID, 0, 10)
	require.NoError(t, err)
	require.Empty(t, versions)
	copies, _, err := lib.ListContentCopies(ctx, observation.Signature, 0, 10)
	require.NoError(t, err)
	require.Len(t, copies, 1)

	// Startup does not recreate an explicitly removed version from unchanged copy inventory.
	require.NoError(t, lib.AutoMigrate())
	versions, _, err = lib.ListFileVersions(ctx, observation.FileID, 0, 10)
	require.NoError(t, err)
	require.Empty(t, versions)
}
