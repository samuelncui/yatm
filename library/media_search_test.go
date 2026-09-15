package library

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestMediaSearchFiltersBeforeStablePagination(t *testing.T) {
	// Mix matching Tape rows with nonmatches and one same-name Volume.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	want := make([]int64, 0, 101)
	for index := 0; index < 201; index++ {
		name := "Other archive"
		if index%2 == 0 {
			name = "Quarterly archive"
		}
		value, err := lib.CreateMedia(ctx, &Media{
			Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: fmt.Sprintf("LTO%04d", index), Name: name,
			Profile:    (&entity.TapeMediaProfile{Format: TapeFormatLTFSV0}).Pack(),
			CreateTime: time.Unix(int64(index%7), 0),
		})
		require.NoError(t, err)
		if index%2 == 0 {
			want = append(want, value.ID)
		}
	}
	_, err := lib.CreateMedia(ctx, &Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: "d2719f91-4f8f-4e55-b06e-6b3392b33197",
		Name: "Quarterly archive", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack(),
	})
	require.NoError(t, err)

	// Every bounded page is already filtered, despite unrelated rows and creation-time ordering.
	after := int64(0)
	found := make([]int64, 0, len(want))
	for {
		rows, more, err := lib.ListMediaPage(ctx, &entity.MediaFilter{
			Kinds: []entity.MediaKind{entity.MediaKind_MEDIA_KIND_TAPE}, Query: "qUaRtErLy",
			Limit: proto.Int64(7), AfterId: &after,
		})
		require.NoError(t, err)
		require.LessOrEqual(t, len(rows), 7)
		for _, row := range rows {
			require.Greater(t, row.ID, after)
			found = append(found, row.ID)
			after = row.ID
		}
		if !more {
			break
		}
		require.Len(t, rows, 7)
	}
	require.Equal(t, want, found)

	// Removing the cursor row does not skip its next matching successor.
	require.NoError(t, lib.DeleteMedia(ctx, want[0]))
	rows, more, err := lib.ListMediaPage(ctx, &entity.MediaFilter{
		Kinds: []entity.MediaKind{entity.MediaKind_MEDIA_KIND_TAPE}, Query: "quarterly",
		Limit: proto.Int64(1), AfterId: &want[0],
	})
	require.NoError(t, err)
	require.True(t, more)
	require.Len(t, rows, 1)
	require.Equal(t, want[1], rows[0].ID)
}

func TestMediaSearchUsesLiteralNameAndIdentity(t *testing.T) {
	// Include characters that would otherwise broaden SQL LIKE matching.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	for index, name := range []string{"Quarterly 100%_! archive", "Quarterly 100AA archive", "literal * ? archive"} {
		_, err := lib.CreateMedia(ctx, &Media{
			Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: fmt.Sprintf("LTO%04d", index), Name: name,
			Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV0}).Pack(),
		})
		require.NoError(t, err)
	}
	_, err := lib.CreateMedia(ctx, &Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: "d2719f91-4f8f-4e55-b06e-6b3392b33197",
		Name: "Volume", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack(),
	})
	require.NoError(t, err)

	// Name and identity matching share case folding and literal punctuation semantics.
	for _, test := range []struct {
		query string
		name  string
	}{
		{query: "100%_!", name: "Quarterly 100%_! archive"},
		{query: "_", name: "Quarterly 100%_! archive"},
		{query: "%", name: "Quarterly 100%_! archive"},
		{query: "!", name: "Quarterly 100%_! archive"},
		{query: "* ?", name: "literal * ? archive"},
		{query: "lTo0001", name: "Quarterly 100AA archive"},
		{query: "4F8F-4E55", name: "Volume"},
		{query: "unknown"},
		{query: "' OR 1=1 --"},
	} {
		t.Run(test.query, func(t *testing.T) {
			rows, more, err := lib.ListMediaPage(ctx, &entity.MediaFilter{Query: test.query, AfterId: proto.Int64(0)})
			require.NoError(t, err)
			require.False(t, more)
			if test.name == "" {
				require.Empty(t, rows)
				return
			}
			require.Len(t, rows, 1)
			require.Equal(t, test.name, rows[0].Name)
		})
	}
}

func TestMediaSearchRejectsInvalidPageBounds(t *testing.T) {
	// Validate every public bound at the Library boundary, including non-HTTP callers.
	_, lib := newTestLibrary(t)
	for _, test := range []struct {
		name   string
		filter *entity.MediaFilter
		want   string
	}{
		{name: "zero limit", filter: &entity.MediaFilter{Limit: proto.Int64(0)}, want: "limit=0"},
		{name: "negative limit", filter: &entity.MediaFilter{Limit: proto.Int64(-1)}, want: "limit=-1"},
		{name: "oversized limit", filter: &entity.MediaFilter{Limit: proto.Int64(1001)}, want: "too large"},
		{name: "negative offset", filter: &entity.MediaFilter{Offset: proto.Int64(-1)}, want: "offset"},
		{name: "negative cursor", filter: &entity.MediaFilter{AfterId: proto.Int64(-1)}, want: "after_id"},
		{name: "mixed cursors", filter: &entity.MediaFilter{Offset: proto.Int64(0), AfterId: proto.Int64(0)}, want: "mutually exclusive"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := lib.ListMediaPage(context.Background(), test.filter)
			require.ErrorContains(t, err, test.want)
		})
	}
}
