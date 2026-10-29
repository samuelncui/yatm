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
	value.Devices[0].GetTape().ReadParameters = &entity.TapeReadParameters{
		AppliesTo: &entity.TapeReadApplicability{DensityCode: proto.Uint32(0),
			RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE.Enum(), RecordBytes: proto.Uint32(262144),
			HardwareCompression: proto.Bool(false), EncryptionAlgorithm: proto.String("")},
		Geometry:        &entity.TapeReadGeometry{MinLpos: proto.Uint64(0), MaxLpos: proto.Uint64(math.MaxUint64)},
		Positioning:     &entity.TapeReadPositioning{BaseSeconds: proto.Float64(0), LandingZoneChangeSeconds: proto.Float64(-6.04)},
		Reading:         &entity.TapeReadTiming{BytesPerSecond: proto.Float64(93_000_000)},
		SourceReference: "Sequential observation; post-LOCATE overhead not measured",
	}
	saved, err := module.Device.Save(ctx, value)
	require.NoError(t, err)
	saved.Devices[0].GetTape().ReadParameters.Reading.BytesPerSecond = nil

	// The existing replacement path preserves explicit false, empty plaintext and numeric zero.
	stored, err := module.Device.Current(ctx)
	require.NoError(t, err)
	require.True(t, proto.Equal(value, stored))
	var document row
	require.NoError(t, module.db.First(&document, "key = ?", "devices").Error)
	for _, field := range []string{`"densityCode":0`, `"hardwareCompression":false`, `"encryptionAlgorithm":""`,
		`"minLpos":"0"`, `"maxLpos":"18446744073709551615"`, `"baseSeconds":0`} {
		require.Contains(t, string(document.Value), field)
	}
	require.NotContains(t, string(document.Value), "postLocateReadOverheadSeconds")
	require.NotContains(t, string(document.Value), "filemarkOverheadSeconds")
	defaults := TapeDefaults(stored, "/dev/inactive")
	defaults.ReadParameters.Geometry.MaxLpos = proto.Uint64(7)
	defaults.ReadParameters.AppliesTo.HardwareCompression = proto.Bool(true)
	require.True(t, proto.Equal(value, stored), "attempt defaults must not mutate committed measurements")

	// Explicit removal returns to absent inputs through the same group replacement.
	value.Devices[0].GetTape().ReadParameters = nil
	_, err = module.Device.Save(ctx, value)
	require.NoError(t, err)
	cleared, err := module.Device.Current(ctx)
	require.NoError(t, err)
	require.Nil(t, cleared.Devices[0].GetTape().ReadParameters)
}

func TestDeviceReadParametersValidateOnlyProvidedValues(t *testing.T) {
	for name, value := range map[string]*entity.TapeReadParameters{
		"absent":                 nil,
		"empty":                  {},
		"one bound":              {Geometry: &entity.TapeReadGeometry{MinLpos: proto.Uint64(0)}},
		"zero wrap without LPOS": {Positioning: &entity.TapeReadPositioning{LongitudinalSecondsPerWrap: proto.Float64(0)}},
		"zero times": {Reading: &entity.TapeReadTiming{PostLocateReadOverheadSeconds: proto.Float64(0),
			FilemarkOverheadSeconds: proto.Float64(0)}},
		"signed CERN terms": {Positioning: &entity.TapeReadPositioning{BaseSeconds: proto.Float64(-1),
			WrapChangeSeconds: proto.Float64(-2), BandChangeSeconds: proto.Float64(-3),
			LandingZoneChangeSeconds: proto.Float64(-6.04), DirectionChangeSeconds: proto.Float64(-4),
			StepBackSeconds: proto.Float64(-5), LongitudinalSecondsPerLpos: proto.Float64(0)}},
	} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, ValidateTapeReadParameters(value))
		})
	}

	// Invalid supplied values cannot replace an otherwise valid inactive-device setting.
	for name, value := range map[string]*entity.TapeReadParameters{
		"blank serial":          {AppliesTo: &entity.TapeReadApplicability{DriveSerial: proto.String("")}},
		"blank firmware":        {AppliesTo: &entity.TapeReadApplicability{Firmware: proto.String(" ")}},
		"density":               {AppliesTo: &entity.TapeReadApplicability{DensityCode: proto.Uint32(256)}},
		"record mode":           {AppliesTo: &entity.TapeReadApplicability{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_UNSPECIFIED.Enum()}},
		"zero record":           {AppliesTo: &entity.TapeReadApplicability{RecordBytes: proto.Uint32(0)}},
		"unaligned record":      {AppliesTo: &entity.TapeReadApplicability{RecordBytes: proto.Uint32(513)}},
		"blank algorithm":       {AppliesTo: &entity.TapeReadApplicability{EncryptionAlgorithm: proto.String(" ")}},
		"zero wraps":            {Geometry: &entity.TapeReadGeometry{NominalWraps: proto.Uint32(0)}},
		"reversed bounds":       {Geometry: &entity.TapeReadGeometry{MinLpos: proto.Uint64(7), MaxLpos: proto.Uint64(6)}},
		"equal bounds":          {Geometry: &entity.TapeReadGeometry{MinLpos: proto.Uint64(7), MaxLpos: proto.Uint64(7)}},
		"NaN coefficient":       {Positioning: &entity.TapeReadPositioning{BaseSeconds: proto.Float64(math.NaN())}},
		"infinite coefficient":  {Positioning: &entity.TapeReadPositioning{StepBackSeconds: proto.Float64(math.Inf(1))}},
		"duration overflow":     {Positioning: &entity.TapeReadPositioning{BaseSeconds: proto.Float64(1e20)}},
		"negative longitudinal": {Positioning: &entity.TapeReadPositioning{LongitudinalSecondsPerLpos: proto.Float64(-1)}},
		"negative normalized":   {Positioning: &entity.TapeReadPositioning{LongitudinalSecondsPerWrap: proto.Float64(-1)}},
		"NaN normalized":        {Positioning: &entity.TapeReadPositioning{LongitudinalSecondsPerWrap: proto.Float64(math.NaN())}},
		"infinite normalized":   {Positioning: &entity.TapeReadPositioning{LongitudinalSecondsPerWrap: proto.Float64(math.Inf(1))}},
		"mixed zero units": {Positioning: &entity.TapeReadPositioning{
			LongitudinalSecondsPerLpos: proto.Float64(0), LongitudinalSecondsPerWrap: proto.Float64(0)}},
		"zero rate":         {Reading: &entity.TapeReadTiming{BytesPerSecond: proto.Float64(0)}},
		"infinite rate":     {Reading: &entity.TapeReadTiming{BytesPerSecond: proto.Float64(math.Inf(1))}},
		"negative overhead": {Reading: &entity.TapeReadTiming{PostLocateReadOverheadSeconds: proto.Float64(-1)}},
		"negative filemark": {Reading: &entity.TapeReadTiming{FilemarkOverheadSeconds: proto.Float64(-1)}},
	} {
		t.Run(name, func(t *testing.T) {
			settings := deviceTestValue()
			settings.Devices[0].GetTape().ReadParameters = value
			require.Error(t, ValidateDevice(settings))
		})
	}
}

func TestDeviceReadParametersRoundTripNormalizedUnits(t *testing.T) {
	// The existing replacement path preserves normalized zero without inventing absolute coordinates.
	module := testModule(t)
	ctx := context.Background()
	for _, seconds := range []float64{0, 107} {
		value := deviceTestValue()
		value.Devices[0].GetTape().ReadParameters = &entity.TapeReadParameters{
			Positioning: &entity.TapeReadPositioning{LongitudinalSecondsPerWrap: proto.Float64(seconds)},
		}
		_, err := module.Device.Save(ctx, value)
		require.NoError(t, err)
		stored, err := module.Device.Current(ctx)
		require.NoError(t, err)
		parameters := stored.Devices[0].GetTape().ReadParameters
		require.Nil(t, parameters.Geometry)
		require.Nil(t, parameters.Positioning.LongitudinalSecondsPerLpos)
		require.NotNil(t, parameters.Positioning.LongitudinalSecondsPerWrap)
		require.Equal(t, seconds, parameters.Positioning.GetLongitudinalSecondsPerWrap())
		require.True(t, proto.Equal(value, stored))
	}
}
