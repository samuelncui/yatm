package settings

import (
	"context"
	"math"
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestDeviceReadParametersPreserveUnknownsAndIsolateDefaults(t *testing.T) {
	// Missing measurements never acquire a nominal rate, zero overhead or CERN preset.
	module := testModule(t)
	ctx := context.Background()
	absent, err := module.Device.Read(ctx)
	require.NoError(t, err)
	require.Nil(t, TapeDefaults(absent.Value, "/dev/inactive").ReadParameters)
	value := deviceTestValue()
	value.Devices[0].GetTape().ReadParameters = []*entity.TapeReadParameters{{
		AppliesTo:       &entity.TapeReadApplicability{DensityCode: proto.Uint32(0)},
		Geometry:        &entity.TapeReadGeometry{NominalWraps: proto.Uint32(80)},
		Positioning:     &entity.TapeReadPositioning{BaseSeconds: proto.Float64(0), ApproachReversalSeconds: proto.Float64(4.3)},
		Reading:         &entity.TapeReadTiming{BytesPerSecond: proto.Float64(93_000_000)},
		SourceReference: "Sequential observation; filemark overhead not measured",
	}, {AppliesTo: &entity.TapeReadApplicability{DensityCode: proto.Uint32(0x58), DriveSerial: proto.String("SN1")}}}
	saved, err := module.Device.Save(ctx, value)
	require.NoError(t, err)
	saved.Devices[0].GetTape().ReadParameters[0].Reading.BytesPerSecond = nil

	// The existing replacement path preserves explicit numeric zero.
	stored, err := module.Device.Current(ctx)
	require.NoError(t, err)
	require.True(t, proto.Equal(value, stored))
	var document row
	require.NoError(t, module.db.First(&document, "key = ?", "devices").Error)
	for _, field := range []string{`"densityCode":0`, `"nominalWraps":80`, `"baseSeconds":0`} {
		require.Contains(t, string(document.Value), field)
	}
	require.NotContains(t, string(document.Value), "filemarkOverheadSeconds")
	defaults := TapeDefaults(stored, "/dev/inactive")
	defaults.ReadParameters[0].Geometry.NominalWraps = proto.Uint32(7)
	defaults.ReadParameters[1].AppliesTo.DriveSerial = proto.String("SN2")
	require.True(t, proto.Equal(value, stored), "attempt defaults must not mutate committed measurements")

	// Explicit removal returns to absent inputs through the same group replacement.
	value.Devices[0].GetTape().ReadParameters = nil
	_, err = module.Device.Save(ctx, value)
	require.NoError(t, err)
	cleared, err := module.Device.Current(ctx)
	require.NoError(t, err)
	require.Nil(t, cleared.Devices[0].GetTape().ReadParameters)
}

// densityParameters returns a set for density code 0x58 with the supplied terms.
func densityParameters(value *entity.TapeReadParameters) *entity.TapeReadParameters {
	value.AppliesTo = &entity.TapeReadApplicability{DensityCode: proto.Uint32(0x58)}
	return value
}

func TestDeviceReadParametersValidateOnlyProvidedValues(t *testing.T) {
	for name, value := range map[string]*entity.TapeReadParameters{
		"density only":       densityParameters(&entity.TapeReadParameters{}),
		"zero seek per wrap": densityParameters(&entity.TapeReadParameters{Positioning: &entity.TapeReadPositioning{SeekSecondsPerWrap: proto.Float64(0)}}),
		"zero times":         densityParameters(&entity.TapeReadParameters{Reading: &entity.TapeReadTiming{FilemarkOverheadSeconds: proto.Float64(0)}}),
		"zero positioning terms": densityParameters(&entity.TapeReadParameters{Positioning: &entity.TapeReadPositioning{BaseSeconds: proto.Float64(0),
			ApproachReversalSeconds: proto.Float64(0), SeekSecondsPerWrap: proto.Float64(0)}}),
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, ValidateTapeReadParameters(value))
		})
	}

	// Every set names a density code, unique within its device.
	settings := deviceTestValue()
	settings.Devices[0].GetTape().ReadParameters = []*entity.TapeReadParameters{densityParameters(&entity.TapeReadParameters{}),
		densityParameters(&entity.TapeReadParameters{SourceReference: "again"})}
	require.ErrorContains(t, ValidateDevice(settings), "repeat density code")

	// Invalid supplied values cannot replace an otherwise valid inactive-device setting.
	for name, value := range map[string]*entity.TapeReadParameters{
		"absent":               nil,
		"no density":           {},
		"serial only":          {AppliesTo: &entity.TapeReadApplicability{DriveSerial: proto.String("SN1")}},
		"blank serial":         {AppliesTo: &entity.TapeReadApplicability{DensityCode: proto.Uint32(0x58), DriveSerial: proto.String(" ")}},
		"density":              {AppliesTo: &entity.TapeReadApplicability{DensityCode: proto.Uint32(256)}},
		"zero wraps":           {Geometry: &entity.TapeReadGeometry{NominalWraps: proto.Uint32(0)}},
		"NaN coefficient":      {Positioning: &entity.TapeReadPositioning{BaseSeconds: proto.Float64(math.NaN())}},
		"infinite coefficient": {Positioning: &entity.TapeReadPositioning{ApproachReversalSeconds: proto.Float64(math.Inf(1))}},
		"duration overflow":    {Positioning: &entity.TapeReadPositioning{BaseSeconds: proto.Float64(1e20)}},
		"negative base":        {Positioning: &entity.TapeReadPositioning{BaseSeconds: proto.Float64(-1)}},
		"negative reversal":    {Positioning: &entity.TapeReadPositioning{ApproachReversalSeconds: proto.Float64(-1)}},
		"negative normalized":  {Positioning: &entity.TapeReadPositioning{SeekSecondsPerWrap: proto.Float64(-1)}},
		"NaN normalized":       {Positioning: &entity.TapeReadPositioning{SeekSecondsPerWrap: proto.Float64(math.NaN())}},
		"infinite normalized":  {Positioning: &entity.TapeReadPositioning{SeekSecondsPerWrap: proto.Float64(math.Inf(1))}},
		"zero rate":            {Reading: &entity.TapeReadTiming{BytesPerSecond: proto.Float64(0)}},
		"infinite rate":        {Reading: &entity.TapeReadTiming{BytesPerSecond: proto.Float64(math.Inf(1))}},
		"negative filemark":    {Reading: &entity.TapeReadTiming{FilemarkOverheadSeconds: proto.Float64(-1)}},
	} {
		t.Run(name, func(t *testing.T) {
			if value != nil && value.AppliesTo == nil && name != "no density" {
				densityParameters(value)
			}
			settings := deviceTestValue()
			settings.Devices[0].GetTape().ReadParameters = []*entity.TapeReadParameters{value}
			require.Error(t, ValidateDevice(settings))
		})
	}
}

func TestDeviceReadParametersRoundTripSeekPerWrap(t *testing.T) {
	// The existing replacement path preserves an explicit zero seek time without inventing geometry.
	module := testModule(t)
	ctx := context.Background()
	for _, seconds := range []float64{0, 107} {
		value := deviceTestValue()
		value.Devices[0].GetTape().ReadParameters = []*entity.TapeReadParameters{densityParameters(&entity.TapeReadParameters{
			Positioning: &entity.TapeReadPositioning{SeekSecondsPerWrap: proto.Float64(seconds)},
		})}
		_, err := module.Device.Save(ctx, value)
		require.NoError(t, err)
		stored, err := module.Device.Current(ctx)
		require.NoError(t, err)
		parameters := stored.Devices[0].GetTape().ReadParameters[0]
		require.Nil(t, parameters.Geometry)
		require.NotNil(t, parameters.Positioning.SeekSecondsPerWrap)
		require.Equal(t, seconds, parameters.Positioning.GetSeekSecondsPerWrap())
		require.True(t, proto.Equal(value, stored))
	}
}
