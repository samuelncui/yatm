package apis_test

import (
	"context"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestMediaSearchAndSelectedIDRevalidation(t *testing.T) {
	// Register a searchable Tape alongside the existing mounted Volume fixture.
	ctx := context.Background()
	fixture := newContentFixture(t)
	tape, err := fixture.lib.CreateMedia(ctx, &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "LTO009", Name: "Travel archive",
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatLTFSV0}).Pack(),
	})
	require.NoError(t, err)

	// Search is performed before the page limit and still honors the requested backend.
	request := (&entity.MediaFilter{
		Query: "TRAVEL", Kinds: []entity.MediaKind{entity.MediaKind_MEDIA_KIND_TAPE},
		Limit: proto.Int64(1), AfterId: proto.Int64(0),
	}).Pack()
	result, err := entity.NewMediaServiceClient(domainConnection(t, fixture.api)).List(ctx, request)
	require.NoError(t, err)
	require.False(t, result.HasMore)
	require.Len(t, result.Media, 1)
	require.Equal(t, tape.ID, result.Media[0].Id)

	// A deleted selection disappears from both name search and explicit identity revalidation.
	_, err = fixture.lib.DeleteMedia(ctx, false, tape.ID)
	require.NoError(t, err)
	result, err = entity.NewMediaServiceClient(domainConnection(t, fixture.api)).List(ctx, request)
	require.NoError(t, err)
	require.Empty(t, result.Media)
	result, err = entity.NewMediaServiceClient(domainConnection(t, fixture.api)).List(ctx, (&entity.MediaIds{Ids: []int64{tape.ID}}).Pack())
	require.NoError(t, err)
	require.Empty(t, result.Media)
}
