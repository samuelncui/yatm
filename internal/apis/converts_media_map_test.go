package apis

import (
	"strings"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestConvertMediaCarriesRequestOwnedTapePositionMap(t *testing.T) {
	// List and detail share this conversion instead of omitting mutable Media facts from one surface.
	stored := &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Profile: (&entity.TapeMediaProfile{
		Format: library.TapeFormatTarPAXV1,
	}).Pack(), TapePositionMap: &entity.TapePositionMap{Source: "retained observations"}}
	converted, err := convertMedia(stored)
	require.NoError(t, err)
	require.True(t, proto.Equal(stored.TapePositionMap, converted.TapePositionMap))

	// Response edits cannot mutate shared Library geometry or the immutable identity profile.
	converted.TapePositionMap.Source = "response edit"
	require.Equal(t, "retained observations", stored.TapePositionMap.Source)
	require.True(t, proto.Equal(stored.Profile, converted.Profile))
}

func TestConvertMediaReportsSavedKeyWithoutReturningIt(t *testing.T) {
	// Responses carry only key presence; the Library profile keeps the key for its own operations.
	key := "v1:" + strings.Repeat("a", 64)
	stored := &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Profile: (&entity.TapeMediaProfile{
		Format: library.TapeFormatTarPAXV1, Encryption: key,
	}).Pack()}
	converted, err := convertMedia(stored)
	require.NoError(t, err)
	require.True(t, converted.EncryptionKeySaved)
	require.Empty(t, converted.Profile.GetTape().Encryption)
	require.Equal(t, key, stored.Profile.GetTape().Encryption)

	// A profile without a key reports none.
	stored.Profile.GetTape().Encryption = ""
	converted, err = convertMedia(stored)
	require.NoError(t, err)
	require.False(t, converted.EncryptionKeySaved)
}
