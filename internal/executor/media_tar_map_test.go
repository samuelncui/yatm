package executor

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
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

func TestTarPositionMapBelongsToTheCurrentReport(t *testing.T) {
	// A current report supplies this Session's map; the model requires both lifetimes.
	stored := tarMapMediaFixture()
	profile := proto.Clone(stored.Profile)
	geometry, cleanup := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 10}, {Wrap: 1, Object: 20}, {Wrap: 2, Object: 24}})
	parameters := tarNormalizedParameters()
	live := new(atomic.Bool)
	live.Store(true)
	model, observed, reason := compileTarMediaReadModel(parameters, describeMedia(stored), geometry, live.Load)
	require.NotNil(t, model, reason)
	require.NotNil(t, observed)
	require.Equal(t, uint64(24), observed.eod[0])
	require.True(t, proto.Equal(profile, stored.Profile))
	score, known := model.AccessScore(&entity.TapeBytePosition{LogicalObject: 24}, &entity.TapeBytePosition{LogicalObject: 20})
	require.True(t, known)
	require.Equal(t, float64(4), score)
	_, known = model.AccessScore(&entity.TapeBytePosition{LogicalObject: 20}, &entity.TapeBytePosition{LogicalObject: 25})
	require.False(t, known, "the map never extends actual EOD")
	live.Store(false)
	require.False(t, model.Valid())
	live.Store(true)
	require.NoError(t, cleanup())
	require.False(t, model.Valid(), "the report's lifetime remains required")

	// Capturing geometry does not require complete timing inputs and cannot turn absence into invented estimates.
	current, _ := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 10}, {Wrap: 1, Object: 20}, {Wrap: 2, Object: 22}})
	parameters.Positioning = nil
	model, observed, reason = compileTarMediaReadModel(parameters, describeMedia(stored), current, live.Load)
	require.Nil(t, model)
	require.NotNil(t, observed)
	require.NotEmpty(t, reason)
}

func TestTarPositionMapRejectsUnusableCurrentReports(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*entity.TapeReadParameters, *library.Media, *tape.WrapMap)
	}{
		{"current wrap gap", func(_ *entity.TapeReadParameters, _ *library.Media, current *tape.WrapMap) {
			current.Endpoints[1].Wrap = 3
		}},
		{"other partition", func(_ *entity.TapeReadParameters, _ *library.Media, current *tape.WrapMap) {
			current.Endpoints[1].Partition = 1
		}},
		{"reversed appended tail", func(_ *entity.TapeReadParameters, _ *library.Media, current *tape.WrapMap) {
			current.Endpoints = append(current.Endpoints, tape.WrapEndpoint{Wrap: 3, Object: 24})
		}},
		{"missing nominal layout", func(parameters *entity.TapeReadParameters, _ *library.Media, _ *tape.WrapMap) {
			parameters.Geometry = nil
		}},
		{"explicit invalid nominal layout", func(parameters *entity.TapeReadParameters, _ *library.Media, _ *tape.WrapMap) {
			parameters.Geometry = &entity.TapeReadGeometry{NominalWraps: proto.Uint32(7)}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parameters := tarNormalizedParameters()
			stored := tarMapMediaFixture()
			current, _ := tarReadModelMap(t, []tape.WrapEndpoint{{Object: 10}, {Wrap: 1, Object: 20}, {Wrap: 2, Object: 24}})
			test.change(parameters, stored, current)
			model, capture, reason := compileTarMediaReadModel(parameters, describeMedia(stored), current, func() bool { return true })
			require.Nil(t, model)
			require.Nil(t, capture)
			require.NotEmpty(t, reason)
		})
	}
}

func TestTarPositionMapNoReportPreservesFallback(t *testing.T) {
	model, capture, reason := compileTarMediaReadModel(tarNormalizedParameters(), describeMedia(tarMapMediaFixture()), nil, func() bool { return true })
	require.Nil(t, model)
	require.Nil(t, capture)
	require.NotEmpty(t, reason)
}
