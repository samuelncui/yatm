package library

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func testTapePositionMap() *entity.TapePositionMap {
	return &entity.TapePositionMap{
		InitializationId: "11111111-1111-4111-8111-111111111111",
		Partitions:       []*entity.TapePositionPartition{{Partition: 0, Eod: proto.Uint64(150)}},
		Wraps: []*entity.TapePositionWrap{
			{Wrap: 0, Direction: entity.TapePositionDirection_TAPE_POSITION_DIRECTION_FORWARD,
				Full: true, Endpoint: proto.Uint64(100), EndpointUncertaintyObjects: proto.Uint64(0)},
			{Wrap: 1, Direction: entity.TapePositionDirection_TAPE_POSITION_DIRECTION_REVERSE,
				Endpoint: proto.Uint64(150), EstimatedSpanObjects: proto.Uint64(100)},
		},
		EndpointConvention: entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE,
		Source:             "derived wrap observations", ObservedAtNs: 1792560000123456789,
	}
}

func TestTapePositionMapJSONLRoundTrip(t *testing.T) {
	// Persist typed geometry separately from the frozen cartridge profile and export its existing Media row.
	ctx := context.Background()
	db, source := newTestLibrary(t)
	_, target := newTestLibrary(t)
	requested := tarTestMedia()
	requested.TapePositionMap = testTapePositionMap()
	media, err := source.CreateMedia(ctx, requested)
	require.NoError(t, err)
	require.True(t, db.Migrator().HasColumn(ModelMedia, "tape_position_map"))
	var backup bytes.Buffer
	require.NoError(t, source.Export(ctx, &backup, []entity.LibraryEntityType{
		entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_MEDIA,
	}))
	require.Contains(t, backup.String(), `"tape_position_map":`)
	require.Contains(t, backup.String(), `"observed_at_ns":"1792560000123456789"`)

	// Import and read the real protobuf BLOB, preserving unknown endpoint uncertainty as absent.
	require.NoError(t, target.Import(ctx, bytes.NewReader(backup.Bytes()), false))
	stored, err := target.GetMedia(ctx, media.ID)
	require.NoError(t, err)
	require.True(t, proto.Equal(media.TapePositionMap, stored.TapePositionMap))
	require.True(t, proto.Equal(media.Profile, stored.Profile))
	require.Nil(t, stored.TapePositionMap.Wraps[1].EndpointUncertaintyObjects)
}

func TestMediaOnlyImportUpdatesMapWithoutRebindingPositions(t *testing.T) {
	// A mutable geometry correction retains the same frozen Media owner of saved copies.
	ctx := context.Background()
	_, source := newTestLibrary(t)
	_, target := newTestLibrary(t)
	media := tarTestMedia()
	media.ID, media.TapePositionMap = 1, testTapePositionMap()
	_, err := target.CreateMedia(ctx, media)
	require.NoError(t, err)
	require.NoError(t, target.SavePosition(ctx, &Position{MediaID: 1, Path: "saved.txt"}))
	media.TapePositionMap = proto.Clone(media.TapePositionMap).(*entity.TapePositionMap)
	media.TapePositionMap.Wraps[0].Endpoint = proto.Uint64(90)
	_, err = source.CreateMedia(ctx, media)
	require.NoError(t, err)
	var backup bytes.Buffer
	require.NoError(t, source.Export(ctx, &backup, []entity.LibraryEntityType{
		entity.LibraryEntityType_LIBRARY_ENTITY_TYPE_MEDIA,
	}))

	// Invalid backup geometry rolls back before the same valid correction is imported.
	invalid := bytes.Replace(backup.Bytes(), []byte(`"endpoint":"90"`), []byte(`"endpoint":"200"`), 1)
	require.Error(t, target.Import(ctx, bytes.NewReader(invalid), false))
	stored, err := target.GetMedia(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(100), *stored.TapePositionMap.Wraps[0].Endpoint)
	require.NoError(t, target.Import(ctx, bytes.NewReader(backup.Bytes()), false))
	stored, err = target.GetMedia(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(90), *stored.TapePositionMap.Wraps[0].Endpoint)
	var positions int64
	require.NoError(t, target.db.Model(ModelPosition).Where("media_id = ?", 1).Count(&positions).Error)
	require.Equal(t, int64(1), positions)
}

func TestUpdateMediaTapePositionMapUsesCurrentIdentityAndMap(t *testing.T) {
	// Capture an initial map and two request snapshots before an explicit correction.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	media, err := lib.CreateMedia(ctx, tarTestMedia())
	require.NoError(t, err)
	first, err := lib.UpdateMediaTapePositionMap(ctx, media, testTapePositionMap())
	require.NoError(t, err)
	stale, err := lib.GetMedia(ctx, media.ID)
	require.NoError(t, err)
	corrected := proto.Clone(first.TapePositionMap).(*entity.TapePositionMap)
	corrected.Wraps[0].Endpoint = proto.Uint64(90)
	corrected.Wraps[1].EstimatedSpanObjects = proto.Uint64(110)

	// Replacing erroneous geometry is permitted without altering immutable profile identity.
	updated, err := lib.UpdateMediaTapePositionMap(ctx, first, corrected)
	require.NoError(t, err)
	require.True(t, proto.Equal(first.Profile, updated.Profile))
	corrected.Wraps[0].Endpoint = proto.Uint64(80)
	require.Equal(t, uint64(90), *updated.TapePositionMap.Wraps[0].Endpoint)
	_, err = lib.UpdateMediaTapePositionMap(ctx, stale, nil)
	require.ErrorContains(t, err, "position map changed")

	// A profile changed by another owner cannot receive even a map invalidation from the old snapshot.
	changed := proto.Clone(updated.Profile).(*entity.MediaProfile)
	changed.GetTape().SerialNumber = "replacement"
	require.NoError(t, db.Model(new(Media)).Where("id = ?", media.ID).Update("profile", changed).Error)
	_, err = lib.UpdateMediaTapePositionMap(ctx, updated, nil)
	require.ErrorContains(t, err, "identity changed")
	current, err := lib.GetMedia(ctx, media.ID)
	require.NoError(t, err)
	cleared, err := lib.UpdateMediaTapePositionMap(ctx, current, nil)
	require.NoError(t, err)
	require.Nil(t, cleared.TapePositionMap)
	stored, err := lib.GetMedia(ctx, media.ID)
	require.NoError(t, err)
	require.Nil(t, stored.TapePositionMap)
}

func TestTapePositionMapRejectsInvalidCoverageAndBinding(t *testing.T) {
	for name, change := range map[string]func(*Media, *entity.TapePositionMap){
		"different initialization": func(_ *Media, value *entity.TapePositionMap) { value.InitializationId = "other" },
		"mixed initialization":     func(_ *Media, value *entity.TapePositionMap) { value.LtfsUuid = "11111111-1111-4111-8111-111111111111" },
		"missing source":           func(_ *Media, value *entity.TapePositionMap) { value.Source = "" },
		"missing convention":       func(_ *Media, value *entity.TapePositionMap) { value.EndpointConvention = 0 },
		"duplicate partition": func(_ *Media, value *entity.TapePositionMap) {
			value.Partitions = append(value.Partitions, value.Partitions[0])
		},
		"missing traversal":     func(_ *Media, value *entity.TapePositionMap) { value.Wraps = nil },
		"missing partition":     func(_ *Media, value *entity.TapePositionMap) { value.Partitions = nil },
		"duplicate wrap":        func(_ *Media, value *entity.TapePositionMap) { value.Wraps[1].Wrap = 0 },
		"unknown direction":     func(_ *Media, value *entity.TapePositionMap) { value.Wraps[0].Direction = 0 },
		"nonzero tar partition": func(_ *Media, value *entity.TapePositionMap) { value.Partitions[0].Partition = 1 },
		"outside EOD":           func(_ *Media, value *entity.TapePositionMap) { value.Wraps[1].Endpoint = proto.Uint64(151) },
		"reversed endpoint":     func(_ *Media, value *entity.TapePositionMap) { value.Wraps[1].Endpoint = proto.Uint64(90) },
		"short estimated span":  func(_ *Media, value *entity.TapePositionMap) { value.Wraps[1].EstimatedSpanObjects = proto.Uint64(49) },
		"full wrap scale":       func(_ *Media, value *entity.TapePositionMap) { value.Wraps[0].EstimatedSpanObjects = proto.Uint64(100) },
		"unknown EOD":           func(_ *Media, value *entity.TapePositionMap) { value.Partitions[0].Eod = nil },
		"incomplete LPOS scale": func(_ *Media, value *entity.TapePositionMap) { value.MinLpos = proto.Uint64(1) },
		"inclusive overflow": func(_ *Media, value *entity.TapePositionMap) {
			value.EndpointConvention = entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_INCLUSIVE
			value.Wraps[0].Endpoint = proto.Uint64(math.MaxUint64)
		},
		"Volume map": func(media *Media, _ *entity.TapePositionMap) {
			media.Kind = entity.MediaKind_MEDIA_KIND_VOLUME
			media.Profile = (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()
		},
	} {
		t.Run(name, func(t *testing.T) {
			media, value := tarTestMedia(), testTapePositionMap()
			change(media, value)
			require.Error(t, ValidateTapePositionMap(media, value))
		})
	}
}

func TestTapePositionMapLTFSBindingAndMissingEndpoints(t *testing.T) {
	// LTFS geometry binds its own UUID, current index generation and declared partition.
	media := &Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE,
		Profile: (&entity.TapeMediaProfile{Format: TapeFormatLTFSV1}).Pack()}
	value := testTapePositionMap()
	value.InitializationId, value.LtfsUuid = "", "22222222-2222-4222-8222-222222222222"
	value.LtfsIndexGeneration = proto.Uint64(1)
	require.NoError(t, ValidateTapePositionMap(media, value))
	value.Wraps[0].Endpoint, value.Wraps[0].EndpointUncertaintyObjects = nil, nil
	value.Wraps[1].EstimatedSpanObjects = nil
	require.NoError(t, ValidateTapePositionMap(media, value))

	// Missing anchors remain gaps, while missing binding evidence cannot establish applicability.
	value.LtfsIndexGeneration = nil
	require.Error(t, ValidateTapePositionMap(media, value))
	value.LtfsIndexGeneration = proto.Uint64(1)
	value.LtfsUuid = "unknown"
	require.Error(t, ValidateTapePositionMap(media, value))
}

func TestMediaJSONRejectsInvalidTapePositionMap(t *testing.T) {
	// The custom Media whitelist carries the optional map through its typed protobuf decoder.
	media := tarTestMedia()
	media.TapePositionMap = testTapePositionMap()
	encoded, err := json.Marshal(media)
	require.NoError(t, err)
	var decoded Media
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.True(t, proto.Equal(media.TapePositionMap, decoded.TapePositionMap))

	// Invalid geometry and unknown map fields fail before replacing the caller's valid value.
	for _, invalid := range [][]byte{
		bytes.Replace(encoded, []byte(`"endpoint":"100"`), []byte(`"endpoint":"200"`), 1),
		bytes.Replace(encoded, []byte(`"source":`), []byte(`"unknown_source":`), 1),
	} {
		require.Error(t, json.Unmarshal(invalid, &decoded))
		require.True(t, proto.Equal(media.TapePositionMap, decoded.TapePositionMap))
	}

	// Export also rejects corrupted stored geometry instead of emitting an unrestorable backup.
	media.TapePositionMap.Wraps[0].Endpoint = proto.Uint64(200)
	_, err = json.Marshal(media)
	require.Error(t, err)
}

func TestMediaJSONAllowsAbsentOrNullTapePositionMap(t *testing.T) {
	// Existing Media without geometry remains serializable, including an explicitly absent map.
	media := tarTestMedia()
	encoded, err := json.Marshal(media)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "tape_position_map")
	for _, data := range [][]byte{encoded, append([]byte(`{"tape_position_map":null,`), encoded[1:]...)} {
		var decoded Media
		require.NoError(t, json.Unmarshal(data, &decoded))
		require.Nil(t, decoded.TapePositionMap)
	}
}
