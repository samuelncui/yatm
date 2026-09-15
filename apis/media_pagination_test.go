package apis_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/stretchr/testify/require"
)

func TestMediaListPagesAndReportsVolumeRuntime(t *testing.T) {
	ctx := context.Background()
	fixture := newOnlineContentFixture(t)
	for index, identity := range []string{"ABC001", "ABC002", "ABC003"} {
		_, err := fixture.lib.CreateMedia(ctx, &library.Media{
			Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: identity, Name: identity,
			Profile:    (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV0}).Pack(),
			CreateTime: time.Unix(int64(index+1), 0),
		})
		require.NoError(t, err)
	}

	limit := int64(2)
	first, err := fixture.api.MediaList(ctx, (&entity.MediaFilter{Limit: &limit}).Pack())
	require.NoError(t, err)
	require.Len(t, first.Media, 2)
	require.True(t, first.HasMore)
	offset := limit
	second, err := fixture.api.MediaList(ctx, (&entity.MediaFilter{Limit: &limit, Offset: &offset}).Pack())
	require.NoError(t, err)
	require.Len(t, second.Media, 2)
	require.False(t, second.HasMore)

	volume, err := fixture.api.MediaList(ctx, (&entity.MediaMGetRequest{Ids: []int64{fixture.media.Id}}).Pack())
	require.NoError(t, err)
	require.Len(t, volume.Media, 1)
	require.NotNil(t, volume.Media[0].Mounted)
	require.True(t, volume.Media[0].GetMounted())
	require.NotNil(t, volume.Media[0].FilesystemAvailableBytes)

	require.NoError(t, os.Rename(
		filepath.Join(fixture.volumeRoot, mediapkg.VolumeMarkerName),
		filepath.Join(fixture.volumeRoot, ".yatm.offline"),
	))
	volume, err = fixture.api.MediaList(ctx, (&entity.MediaMGetRequest{Ids: []int64{fixture.media.Id}}).Pack())
	require.NoError(t, err)
	require.Len(t, volume.Media, 1)
	require.NotNil(t, volume.Media[0].Mounted)
	require.False(t, volume.Media[0].GetMounted())
	require.Nil(t, volume.Media[0].FilesystemAvailableBytes)
}

func TestMediaPositionsUseStablePathPagination(t *testing.T) {
	ctx := context.Background()
	fixture := newOnlineContentFixture(t)
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		fixture.addPosition(t, name, []byte(name))
	}

	limit := int64(2)
	first, err := fixture.api.MediaGetPositions(ctx, &entity.MediaGetPositionsRequest{
		Id: fixture.media.Id, Limit: &limit,
	})
	require.NoError(t, err)
	require.Len(t, first.Positions, 2)
	require.True(t, first.HasMore)
	require.Equal(t, []string{"a.txt", "b.txt"}, []string{first.Positions[0].Path, first.Positions[1].Path})
	after := first.Positions[1].Path
	second, err := fixture.api.MediaGetPositions(ctx, &entity.MediaGetPositionsRequest{
		Id: fixture.media.Id, Limit: &limit, AfterPath: &after,
	})
	require.NoError(t, err)
	require.Len(t, second.Positions, 1)
	require.False(t, second.HasMore)
	require.Equal(t, "c.txt", second.Positions[0].Path)
}

func TestVolumeInspectRejectsMarkerProfileConflict(t *testing.T) {
	fixture := newOnlineContentFixture(t)
	markerPath := filepath.Join(fixture.volumeRoot, mediapkg.VolumeMarkerName)
	data, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	marker := new(mediapkg.VolumeMarker)
	require.NoError(t, json.Unmarshal(data, marker))
	marker.Profile.Type = entity.VolumeType_VOLUME_TYPE_HM_SMR
	data, err = json.MarshalIndent(marker, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(markerPath, append(data, '\n'), 0o644))

	_, err = fixture.api.MediaInspect(context.Background(), (&entity.MediaInspectVolumeTarget{
		Uuid: fixture.media.Identity,
	}).Pack())
	require.ErrorContains(t, err, "marker conflicts with Library")
}
