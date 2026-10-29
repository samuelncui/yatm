package executor

import (
	"testing"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/media/tape"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// tarTestReadParameters assembles the production bootstrap for tar fixtures.
func tarTestReadParameters(report *entity.InspectDeviceResponse,
	profile *entity.TarMediaProfile,
) *entity.TapeReadParameters {
	// A written compressed profile does not establish the within-wrap linear coordinates this prior needs.
	if report.GetTape() == nil || profile == nil || profile.HardwareCompression ||
		profile.RecordMode == entity.TapeRecordMode_TAPE_RECORD_MODE_UNSPECIFIED || profile.RecordBytes == 0 {
		return nil
	}

	return builtinTapeReadParameters(report, observedTarReadApplicability(report, &entity.TapeMediaProfile{Tar: profile}))
}

func TestBuiltinTarReadParametersUseLoadedMediaDensity(t *testing.T) {
	// The same newer drive may read an older formatted cartridge; its name cannot choose geometry.
	_, report, profile := tarReadParametersFixture()
	report.Model = proto.String("ULT3580-TD9")
	for _, test := range []struct {
		density, wraps uint32
		rate           float64
	}{
		{0x40, 48, 20e6}, {0x42, 64, 30e6}, {0x44, 44, 80e6}, {0x46, 56, 120e6},
		{0x58, 80, 140e6}, {0x5a, 136, 160e6}, {0x5c, 112, 300e6}, {0x5d, 168, 300e6},
		{0x5e, 208, 300e6}, {0x60, 280, 300e6},
	} {
		report.GetTape().DensityCode = proto.Uint32(test.density)
		parameters := tarTestReadParameters(report, profile)
		require.NoError(t, settingspkg.ValidateTapeReadParameters(parameters))
		require.Empty(t, tarReadParametersReason(parameters, report, &entity.TapeMediaProfile{Tar: profile}))
		require.Equal(t, test.wraps, parameters.Geometry.GetNominalWraps())
		require.Equal(t, test.rate, parameters.Reading.GetBytesPerSecond())
		require.Nil(t, parameters.Geometry.MinLpos)
		require.Nil(t, parameters.Geometry.MaxLpos)
		require.Nil(t, parameters.Reading.FilemarkOverheadSeconds)
		require.Contains(t, parameters.SourceReference, "unmeasured")
		require.Contains(t, parameters.SourceReference, "reference")
	}
}

func TestBuiltinTarReadParametersPreserveUnknownFactsAndInputs(t *testing.T) {
	// A known media density still permits a generic empirical prior when the vendor/model is unknown.
	_, report, profile := tarReadParametersFixture()
	report.GetTape().DensityCode = proto.Uint32(0x58)
	originalReport, originalProfile := proto.Clone(report), proto.Clone(profile)
	parameters := tarTestReadParameters(report, profile)
	require.Empty(t, tarReadParametersReason(parameters, report, &entity.TapeMediaProfile{Tar: profile}))
	require.NotNil(t, parameters.Positioning.LongitudinalSecondsPerWrap)
	require.Nil(t, parameters.Positioning.LongitudinalSecondsPerLpos)
	*parameters.AppliesTo.DriveSerial = "changed"
	*parameters.AppliesTo.DensityCode = 0x60
	require.True(t, proto.Equal(originalReport, report))
	require.True(t, proto.Equal(originalProfile, profile))

	// Absent observations cannot acquire a fabricated applicability match or physical layout.
	report.SerialNumber, report.Firmware, report.GetTape().DensityCode = nil, nil, nil
	parameters = tarTestReadParameters(report, profile)
	require.Nil(t, parameters.AppliesTo.DriveSerial)
	require.Nil(t, parameters.AppliesTo.Firmware)
	require.Nil(t, parameters.AppliesTo.DensityCode)
	require.Nil(t, parameters.Geometry)
	require.NotEmpty(t, tarReadParametersReason(parameters, report, &entity.TapeMediaProfile{Tar: profile}))
	report.GetTape().DensityCode = proto.Uint32(0xee)
	parameters = tarTestReadParameters(report, profile)
	require.Nil(t, parameters.Geometry, "unrecognized density cannot silently reuse an LTO layout")
	require.Contains(t, parameters.SourceReference, "no sourced layout")

	// The compressed-profile limit belongs to stored write semantics, not current DCE observations.
	profile.HardwareCompression = true
	require.Nil(t, tarTestReadParameters(report, profile))
	require.Nil(t, tarTestReadParameters(nil, profile))
	require.Nil(t, tarTestReadParameters(report, nil))
	profile.HardwareCompression = false
	profile.RecordMode = entity.TapeRecordMode_TAPE_RECORD_MODE_UNSPECIFIED
	require.Nil(t, tarTestReadParameters(report, profile))
	profile.RecordMode = entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE
	profile.RecordBytes = 0
	require.Nil(t, tarTestReadParameters(report, profile))
}

func TestBuiltinTarReadParametersCompileUsefulObservedPositioning(t *testing.T) {
	// Real compiler boundaries consume independently supplied reported objects, never nominal-capacity division.
	_, report, profile := tarReadParametersFixture()
	report.GetTape().DensityCode = proto.Uint32(0x58)
	parameters := tarTestReadParameters(report, profile)
	geometry, cleanup := tarReadModelMap(t, []tape.WrapEndpoint{
		{Wrap: 0, Object: 99}, {Wrap: 1, Object: 189}, {Wrap: 2, Object: 240},
	})
	model, reason := tarTestReadModel(parameters, profile, geometry, mediapkg.ReadEndpointInclusive)
	require.NotNil(t, model, reason)
	score, known := model.AccessScore(&entity.TapeBytePosition{LogicalObject: 1}, &entity.TapeBytePosition{LogicalObject: 170})
	require.True(t, known)
	require.Positive(t, score)
	_, known = model.AccessScore(&entity.TapeBytePosition{LogicalObject: 1}, &entity.TapeBytePosition{LogicalObject: 241})
	require.False(t, known, "nominal geometry cannot extend the actual EOD")

	// Cleanup must invalidate the exact observed map even for a reusable source-backed parameter table.
	require.NoError(t, cleanup())
	require.False(t, model.Valid())
}

func TestBuiltinTarReadParametersPreserveEncryptionApplicability(t *testing.T) {
	for _, test := range []struct {
		name      string
		algorithm *string
	}{
		{"observed algorithm", proto.String("AES-256-GCM-128")},
		{"unknown algorithm", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Only an observed label algorithm establishes encryption applicability.
			_, report, tar := tarReadParametersFixture()
			report.GetTape().DensityCode = proto.Uint32(0x58)
			tar.EncryptionAlgorithm = test.algorithm
			profile := &entity.TapeMediaProfile{Tar: tar, Encryption: "synthetic saved key"}
			scope := observedTarReadApplicability(report, profile)
			parameters := builtinTapeReadParameters(report, scope)
			require.NotNil(t, parameters)

			// Initial timing priors cannot turn an unknown condition into an applicable model.
			if test.algorithm == nil {
				require.Nil(t, scope.EncryptionAlgorithm)
				require.Nil(t, parameters.AppliesTo.EncryptionAlgorithm)
				require.Contains(t, tarReadParametersReason(parameters, report, profile), "encryption algorithm applicability is unknown")
				return
			}
			require.Empty(t, tarReadParametersReason(parameters, report, profile))
			require.Equal(t, "AES-256-GCM-128", parameters.AppliesTo.GetEncryptionAlgorithm())
		})
	}
}
