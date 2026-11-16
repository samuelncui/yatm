package executor

import (
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/tape"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestBuiltinTarReadParametersUseLoadedMediaDensity(t *testing.T) {
	// The same newer drive may read an older formatted cartridge; its name cannot choose geometry.
	_, report, _ := tarReadParametersFixture()
	report.Model = proto.String("ULT3580-TD9")
	for _, test := range []struct {
		density, wraps uint32
		rate           float64
	}{
		{0x40, 48, 20e6}, {0x42, 64, 30e6}, {0x44, 44, 80e6}, {0x46, 56, 120e6},
		{0x58, 80, 140e6}, {0x5a, 136, 160e6}, {0x5c, 112, 300e6}, {0x5d, 168, 300e6},
		{0x5e, 208, 300e6}, {0x60, 280, 300e6}, {0x62, 472, 400e6}, {0x63, 472, 400e6},
	} {
		report.GetTape().DensityCode = proto.Uint32(test.density)
		parameters := builtinTapeReadParameters(report)
		require.NoError(t, settingspkg.ValidateTapeReadParameters(parameters))
		require.Equal(t, test.density, parameters.AppliesTo.GetDensityCode())
		require.Equal(t, test.wraps, parameters.Geometry.GetNominalWraps())
		require.Equal(t, test.rate, parameters.Reading.GetBytesPerSecond())
		require.Nil(t, parameters.Reading.FilemarkOverheadSeconds)
		require.Equal(t, tarBuiltinSourceReference, parameters.SourceReference)
	}
}

func TestBuiltinTapeReadParametersListPlanningValuesForEveryLayout(t *testing.T) {
	// The listed sets are exactly what read planning selects for a cartridge of each density without a configured set.
	_, report, _ := tarReadParametersFixture()
	listed := BuiltinTapeReadParameters()
	require.Len(t, listed, len(tarDensities))
	previous := -1
	for _, parameters := range listed {
		density := parameters.GetAppliesTo().GetDensityCode()
		require.Greater(t, int(density), previous, "ordered by density code")
		previous = int(density)
		require.Nil(t, parameters.GetAppliesTo().DriveSerial)
		require.Equal(t, tarBuiltinSourceReference, parameters.SourceReference)
		report.GetTape().DensityCode = proto.Uint32(density)
		planned, source, _ := selectTapeReadParameters(&entity.DeviceSettings{}, "/dev/nst0", report)
		require.Equal(t, "builtin", source)
		require.True(t, proto.Equal(planned, parameters), "density 0x%x", density)
	}
}

func TestBuiltinTarReadParametersPreserveUnknownFactsAndInputs(t *testing.T) {
	// A known media density still permits a generic empirical prior when the vendor/model is unknown.
	_, report, _ := tarReadParametersFixture()
	report.GetTape().DensityCode = proto.Uint32(0x58)
	originalReport := proto.Clone(report)
	parameters := builtinTapeReadParameters(report)
	require.NotNil(t, parameters.Positioning.SeekSecondsPerWrap)
	*parameters.AppliesTo.DensityCode = 0x60
	require.True(t, proto.Equal(originalReport, report))

	// An absent density cannot acquire a physical layout, nor an unrecognized one an LTO layout.
	report.GetTape().DensityCode = nil
	parameters = builtinTapeReadParameters(report)
	require.Nil(t, parameters.AppliesTo.DensityCode)
	require.Nil(t, parameters.Geometry)
	report.GetTape().DensityCode = proto.Uint32(0xee)
	parameters = builtinTapeReadParameters(report)
	require.Nil(t, parameters.Geometry, "unrecognized density cannot silently reuse an LTO layout")
	require.Equal(t, tarBuiltinSourceReference, parameters.SourceReference)
}

func TestTarReadModelRequiresKnownRecords(t *testing.T) {
	// The written Media profile withholds linear interpolation when its record policy is unknown.
	_, report, profile := tarReadParametersFixture()
	report.GetTape().DensityCode = proto.Uint32(0x58)
	parameters := builtinTapeReadParameters(report)
	geometry, _ := tarReadModelMap(t, []tape.WrapEndpoint{{Wrap: 0, Object: 99}, {Wrap: 1, Object: 189}, {Wrap: 2, Object: 240}})
	for name, change := range map[string]func(*entity.TarMediaProfile){
		"unknown record mode": func(p *entity.TarMediaProfile) { p.RecordMode = entity.TapeRecordMode_TAPE_RECORD_MODE_UNSPECIFIED },
		"unknown record size": func(p *entity.TarMediaProfile) { p.RecordBytes = 0 },
	} {
		changed := proto.Clone(profile).(*entity.TarMediaProfile)
		change(changed)
		model, reason := tarTestReadModel(parameters, changed, geometry)
		require.Nil(t, model, name)
		require.Contains(t, reason, "known written record size", name)
	}
	model, reason := tarTestReadModel(parameters, profile, geometry)
	require.NotNil(t, model, reason)
}

func TestBuiltinTarReadParametersCompileUsefulObservedPositioning(t *testing.T) {
	// Real compiler boundaries consume independently supplied reported objects, never nominal-capacity division.
	_, report, profile := tarReadParametersFixture()
	report.GetTape().DensityCode = proto.Uint32(0x58)
	parameters := builtinTapeReadParameters(report)
	geometry, cleanup := tarReadModelMap(t, []tape.WrapEndpoint{
		{Wrap: 0, Object: 99}, {Wrap: 1, Object: 189}, {Wrap: 2, Object: 240},
	})
	model, reason := tarTestReadModel(parameters, profile, geometry)
	require.NotNil(t, model, reason)
	// A move behind the head is never within the streaming distance.
	score, known := model.AccessScore(&entity.TapeBytePosition{LogicalObject: 170}, &entity.TapeBytePosition{LogicalObject: 1})
	require.True(t, known)
	require.Positive(t, score)
	_, known = model.AccessScore(&entity.TapeBytePosition{LogicalObject: 1}, &entity.TapeBytePosition{LogicalObject: 241})
	require.False(t, known, "nominal geometry cannot extend the actual EOD")

	// Cleanup must invalidate the exact observed map even for a reusable source-backed parameter table.
	require.NoError(t, cleanup())
	require.False(t, model.Valid())
}
