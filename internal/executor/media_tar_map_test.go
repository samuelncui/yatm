package executor

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func tarMapMediaFixture() *library.Media {
	_, _, profile := tarReadParametersFixture()
	profile.InitializationId = "11111111-1111-4111-8111-111111111111"
	return &library.Media{Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: "TAR001",
		Profile: (&entity.TapeMediaProfile{Format: library.TapeFormatTarPAXV1, Encryption: "v1:" + strings.Repeat("ab", 32), Tar: profile}).Pack()}
}

func TestTarPositionMapSurvivesSessionRestart(t *testing.T) {
	// Derive one current map and persist it through the same Library mutation used by the factory.
	ctx := context.Background()
	exe, _, _, _ := tarFactoryFixture(t)
	stored, err := exe.Lib().CreateMedia(ctx, tarMapMediaFixture())
	require.NoError(t, err)
	geometry, cleanup := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 9}, {Wrap: 1, Object: 19}, {Wrap: 2, Object: 24}})
	parameters := tarNormalizedParameters()
	live := new(atomic.Bool)
	live.Store(true)
	first, observed, reason := compileTarMediaReadModel(parameters, stored, geometry, mediapkg.ReadEndpointInclusive, live.Load)
	require.NotNil(t, first, reason)
	require.NotNil(t, observed)
	require.NotNil(t, observed.Wraps[0].EndpointUncertaintyObjects)
	require.Zero(t, *observed.Wraps[0].EndpointUncertaintyObjects)
	_, err = exe.Lib().UpdateMediaTapePositionMap(ctx, stored, observed)
	require.NoError(t, err)
	require.NoError(t, cleanup())
	require.False(t, first.Valid())

	// A new session binds saved spatial facts to its own lifetime without requiring another REOWP report.
	reloaded, err := exe.Lib().GetMedia(ctx, stored.ID)
	require.NoError(t, err)
	parameters.Geometry = nil // Persisted Media owns the spatial layout; drive timings remain independent.
	second, capture, reason := compileTarMediaReadModel(parameters, reloaded, nil, mediapkg.ReadEndpointInclusive, live.Load)
	require.NotNil(t, second, reason)
	require.Nil(t, capture)
	start := &entity.TapeBytePosition{LogicalObject: 20}
	eod := &entity.TapeBytePosition{LogicalObject: 24}
	score, known := second.AccessScore(start, eod)
	require.True(t, known)
	require.Equal(t, float64(4), score)
	_, known = second.AccessScore(start, &entity.TapeBytePosition{LogicalObject: 25})
	require.False(t, known, "older coverage cannot price a later APPEND tail")
	live.Store(false)
	require.False(t, second.Valid(), "the prior session's lifetime never authorizes the next session")
}

func TestTarPositionMapRejectsWrongInitializationAndInterpretation(t *testing.T) {
	// Retain a valid map independently of the report that supplied it.
	stored := tarMapMediaFixture()
	geometry, cleanup := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 9}, {Wrap: 1, Object: 19}, {Wrap: 2, Object: 24}})
	parameters := tarNormalizedParameters()
	value, reason := deriveTarPositionMap(parameters, stored.Profile.GetTape().Tar, geometry, mediapkg.ReadEndpointInclusive)
	require.NotNil(t, value, reason)
	require.NoError(t, cleanup())

	// Saved geometry cannot override a different initialization, endpoint convention or written profile.
	for name, change := range map[string]func(*library.Media){
		"different initialization": func(media *library.Media) { media.TapePositionMap.InitializationId = "different" },
		"unknown endpoint interpretation": func(media *library.Media) {
			media.TapePositionMap.EndpointConvention = entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_UNSPECIFIED
		},
		"outside final EOD":              func(media *library.Media) { media.TapePositionMap.Wraps[2].Endpoint = proto.Uint64(25) },
		"compressed written profile":     func(media *library.Media) { media.Profile.GetTape().Tar.HardwareCompression = true },
		"invalid written record profile": func(media *library.Media) { media.Profile.GetTape().Tar.RecordBytes = 513 },
		"unbounded spatial error": func(media *library.Media) {
			media.TapePositionMap.Wraps[0].EndpointUncertaintyObjects = proto.Uint64(10)
		},
	} {
		t.Run(name, func(t *testing.T) {
			media := tarMapMediaFixture()
			media.TapePositionMap = proto.Clone(value).(*entity.TapePositionMap)
			change(media)
			model, capture, reason := compileTarMediaReadModel(parameters, media, nil,
				mediapkg.ReadEndpointInclusive, func() bool { return true })
			require.Nil(t, model)
			require.Nil(t, capture)
			require.NotEmpty(t, reason)
		})
	}
}

func TestTarPositionMapCurrentReportReplacesOnlyDerivedGeometry(t *testing.T) {
	// An earlier saved EOD remains bounded until a current identified report replaces it.
	stored := tarMapMediaFixture()
	parameters := tarNormalizedParameters()
	earlier, _ := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 9}, {Wrap: 1, Object: 19}, {Wrap: 2, Object: 24}})
	value, reason := deriveTarPositionMap(parameters, stored.Profile.GetTape().Tar, earlier, mediapkg.ReadEndpointInclusive)
	require.NotNil(t, value, reason)
	stored.TapePositionMap = value
	profile := proto.Clone(stored.Profile)
	current, _ := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 9}, {Wrap: 1, Object: 19}, {Wrap: 2, Object: 22}})
	model, capture, reason := compileTarMediaReadModel(parameters, stored, current,
		mediapkg.ReadEndpointInclusive, func() bool { return true })
	require.NotNil(t, model, reason)
	require.Equal(t, uint64(22), *capture.Partitions[0].Eod)
	require.Equal(t, uint64(24), *stored.TapePositionMap.Partitions[0].Eod, "compilation cannot mutate a cached Media")
	require.True(t, proto.Equal(profile, stored.Profile))
	_, known := model.AccessScore(&entity.TapeBytePosition{LogicalObject: 20}, &entity.TapeBytePosition{LogicalObject: 23})
	require.False(t, known)

	// Capturing geometry does not require complete timing inputs and cannot turn absence into invented estimates.
	parameters.Positioning = nil
	model, capture, reason = compileTarMediaReadModel(parameters, stored, current,
		mediapkg.ReadEndpointInclusive, func() bool { return true })
	require.Nil(t, model)
	require.NotNil(t, capture)
	require.NotEmpty(t, reason)
}

func TestTarPositionMapCurrentReportConfirmsRetainedLayout(t *testing.T) {
	for _, test := range []struct {
		name              string
		layout            *entity.TapeReadGeometry
		retainedExclusive bool
		boundary          mediapkg.ReadEndpointConvention
		objects           []uint64
	}{
		{"matching report", nil, false, mediapkg.ReadEndpointInclusive, []uint64{9, 19, 24}},
		{"missing nominal count", &entity.TapeReadGeometry{}, false, mediapkg.ReadEndpointInclusive, []uint64{9, 19, 24}},
		{"retained exclusive", nil, true, mediapkg.ReadEndpointInclusive, []uint64{9, 19, 24}},
		{"reported exclusive", nil, false, mediapkg.ReadEndpointExclusive, []uint64{10, 20, 24}},
		{"longer partial tail", nil, false, mediapkg.ReadEndpointInclusive, []uint64{9, 19, 28}},
		{"former partial now full", nil, false, mediapkg.ReadEndpointInclusive, []uint64{9, 19, 29, 34}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Saved coverage owns its layout after the observing session ends.
			stored := tarMapMediaFixture()
			parameters := tarNormalizedParameters()
			earlier, cleanup := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 9}, {Wrap: 1, Object: 19}, {Wrap: 2, Object: 24}})
			value, reason := deriveTarPositionMap(parameters, stored.Profile.GetTape().Tar, earlier, mediapkg.ReadEndpointInclusive)
			require.NotNil(t, value, reason)
			require.NoError(t, cleanup())
			if test.retainedExclusive {
				value.EndpointConvention = entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE
				for _, wrap := range value.Wraps {
					if wrap.Full {
						*wrap.Endpoint++
					}
				}
			}
			stored.TapePositionMap = value
			before := proto.Clone(value)
			parameters.Geometry = test.layout
			live := new(atomic.Bool)
			live.Store(true)
			baseline, capture, reason := compileTarMediaReadModel(parameters, stored, nil, test.boundary, live.Load)
			require.NotNil(t, baseline, reason)
			require.Nil(t, capture)

			// A confirming live report preserves the same usable coverage without deriving a nominal wrap count.
			endpoints := make([]tape.WrapEndpoint, len(test.objects))
			for index, object := range test.objects {
				endpoints[index] = tape.WrapEndpoint{Wrap: uint32(index), Object: object}
			}
			current, closeCurrent := tarReadModelMap(t, endpoints)
			model, capture, reason := compileTarMediaReadModel(parameters, stored, current, test.boundary, live.Load)
			require.NotNil(t, model, reason)
			require.Nil(t, capture, "confirmation cannot publish old coverage as a new observation")
			start, eod := &entity.TapeBytePosition{LogicalObject: 20}, &entity.TapeBytePosition{LogicalObject: 24}
			expected, known := baseline.AccessScore(start, eod)
			require.True(t, known)
			score, known := model.AccessScore(start, eod)
			require.True(t, known)
			require.Equal(t, expected, score)
			require.Equal(t, float64(4), score)
			_, known = model.AccessScore(start, &entity.TapeBytePosition{LogicalObject: 25})
			require.False(t, known, "current append coverage never extends the retained map")
			require.True(t, proto.Equal(before, stored.TapePositionMap))
			require.Equal(t, endpoints, current.Endpoints)
			require.NoError(t, closeCurrent())
			require.False(t, model.Valid(), "the confirming report's lifetime remains required")
			require.True(t, baseline.Valid())
			live.Store(false)
			require.False(t, baseline.Valid())
		})
	}
}

func TestTarPositionMapCurrentReportCannotReenableContradictoryCoverage(t *testing.T) {
	// A self-described retained map is usable without a report, but it cannot overrule current evidence.
	stored := tarMapMediaFixture()
	parameters := tarNormalizedParameters()
	earlier, cleanup := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 9}, {Wrap: 1, Object: 19}, {Wrap: 2, Object: 24}})
	value, reason := deriveTarPositionMap(parameters, stored.Profile.GetTape().Tar, earlier, mediapkg.ReadEndpointInclusive)
	require.NotNil(t, value, reason)
	require.NoError(t, cleanup())
	for _, test := range []struct {
		name   string
		change func(*entity.TapeReadParameters, *library.Media, *tape.WrapMap, *mediapkg.ReadEndpointConvention)
	}{
		{"different full endpoint", func(_ *entity.TapeReadParameters, _ *library.Media, current *tape.WrapMap, _ *mediapkg.ReadEndpointConvention) {
			current.Endpoints[0].Object = 8
		}},
		{"shorter final EOD", func(_ *entity.TapeReadParameters, _ *library.Media, current *tape.WrapMap, _ *mediapkg.ReadEndpointConvention) {
			current.Endpoints[2].Object = 23
		}},
		{"former partial crosses current boundary", func(_ *entity.TapeReadParameters, _ *library.Media, current *tape.WrapMap, _ *mediapkg.ReadEndpointConvention) {
			current.Endpoints[2].Object = 22
			current.Endpoints = append(current.Endpoints, tape.WrapEndpoint{Wrap: 3, Object: 34})
		}},
		{"current wrap gap", func(_ *entity.TapeReadParameters, _ *library.Media, current *tape.WrapMap, _ *mediapkg.ReadEndpointConvention) {
			current.Endpoints[1].Wrap = 3
		}},
		{"other partition", func(_ *entity.TapeReadParameters, _ *library.Media, current *tape.WrapMap, _ *mediapkg.ReadEndpointConvention) {
			current.Endpoints[1].Partition = 1
		}},
		{"reversed appended tail", func(_ *entity.TapeReadParameters, _ *library.Media, current *tape.WrapMap, _ *mediapkg.ReadEndpointConvention) {
			current.Endpoints = append(current.Endpoints, tape.WrapEndpoint{Wrap: 3, Object: 24})
		}},
		{"inclusive overflow", func(_ *entity.TapeReadParameters, _ *library.Media, current *tape.WrapMap, _ *mediapkg.ReadEndpointConvention) {
			current.Endpoints[0].Object = math.MaxUint64
		}},
		{"unknown current interpretation", func(_ *entity.TapeReadParameters, _ *library.Media, _ *tape.WrapMap, boundary *mediapkg.ReadEndpointConvention) {
			*boundary = 0
		}},
		{"retained wrap gap", func(_ *entity.TapeReadParameters, stored *library.Media, _ *tape.WrapMap, _ *mediapkg.ReadEndpointConvention) {
			stored.TapePositionMap.Wraps[1].Wrap = 3
		}},
		{"different initialization", func(_ *entity.TapeReadParameters, stored *library.Media, _ *tape.WrapMap, _ *mediapkg.ReadEndpointConvention) {
			stored.TapePositionMap.InitializationId = "different"
		}},
		{"explicit invalid nominal layout", func(parameters *entity.TapeReadParameters, _ *library.Media, _ *tape.WrapMap, _ *mediapkg.ReadEndpointConvention) {
			parameters.Geometry = &entity.TapeReadGeometry{NominalWraps: proto.Uint32(7)}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parameters := tarNormalizedParameters()
			parameters.Geometry = nil
			stored := tarMapMediaFixture()
			stored.TapePositionMap = proto.Clone(value).(*entity.TapePositionMap)
			current, _ := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 9}, {Wrap: 1, Object: 19}, {Wrap: 2, Object: 24}})
			boundary := mediapkg.ReadEndpointInclusive
			test.change(parameters, stored, current, &boundary)
			model, capture, reason := compileTarMediaReadModel(parameters, stored, current, boundary, func() bool { return true })
			require.Nil(t, model)
			require.Nil(t, capture)
			require.NotEmpty(t, reason)
		})
	}
}

func TestTarPositionMapNoReportOrSavedMapPreservesFallback(t *testing.T) {
	model, capture, reason := compileTarMediaReadModel(tarNormalizedParameters(), tarMapMediaFixture(), nil,
		mediapkg.ReadEndpointInclusive, func() bool { return true })
	require.Nil(t, model)
	require.Nil(t, capture)
	require.NotEmpty(t, reason)
}

func TestTarPositionMapDescriptorOwnsItsGeometry(t *testing.T) {
	stored := tarMapMediaFixture()
	stored.TapePositionMap = &entity.TapePositionMap{Source: "retained observations"}
	converted := describeMedia(stored)
	require.True(t, proto.Equal(stored.TapePositionMap, converted.TapePositionMap))
	converted.TapePositionMap.Source = "session edit"
	require.Equal(t, "retained observations", stored.TapePositionMap.Source)
}

func TestTarPositionMapUsesRetainedEndpointInterpretation(t *testing.T) {
	stored := tarMapMediaFixture()
	parameters := tarNormalizedParameters()
	geometry, cleanup := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 9}, {Wrap: 1, Object: 19}, {Wrap: 2, Object: 24}})
	value, reason := deriveTarPositionMap(parameters, stored.Profile.GetTape().Tar, geometry, mediapkg.ReadEndpointInclusive)
	require.NotNil(t, value, reason)
	require.NoError(t, cleanup())
	value.EndpointConvention = entity.TapePositionEndpointConvention_TAPE_POSITION_ENDPOINT_CONVENTION_EXCLUSIVE
	for _, wrap := range value.Wraps {
		if wrap.Full {
			*wrap.Endpoint++
		}
	}
	value.Wraps[0].EndpointUncertaintyObjects = proto.Uint64(1)
	stored.TapePositionMap = value
	// The prepared REOWP protocol is inclusive; a retained inferred map independently declares exclusive endpoints.
	model, observed, reason := compileTarMediaReadModel(parameters, stored, nil, mediapkg.ReadEndpointInclusive, func() bool { return true })
	require.NotNil(t, model, reason)
	require.Nil(t, observed)
	_, known := model.AccessCost(&entity.TapeBytePosition{LogicalObject: 2}, &entity.TapeBytePosition{LogicalObject: 18})
	require.True(t, known)
	_, known = model.AccessCost(&entity.TapeBytePosition{LogicalObject: 2}, &entity.TapeBytePosition{LogicalObject: 10})
	require.False(t, known, "uncertain boundary classification remains unavailable")
}
