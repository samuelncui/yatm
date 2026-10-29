package executor

import (
	"testing"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestTarLearnedParametersUseExactScopeAndPreserveManualInputs(t *testing.T) {
	// Hardware observations match one model independently of the configured pathname.
	profile := &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_VARIABLE, RecordBytes: 524288,
		EncryptionAlgorithm: proto.String("AES-256-GCM-128")}
	report := &entity.InspectDeviceResponse{SerialNumber: proto.String("serial"), Firmware: proto.String("firmware"),
		Report: &entity.InspectDeviceResponse_Tape{Tape: &entity.TapeDeviceReport{DensityCode: proto.Uint32(0x58)}}}
	parameters := &entity.TapeReadParameters{AppliesTo: &entity.TapeReadApplicability{
		DriveSerial: proto.String("serial"), Firmware: proto.String("firmware"), DensityCode: proto.Uint32(0x58),
		RecordMode: profile.RecordMode.Enum(), RecordBytes: proto.Uint32(profile.RecordBytes),
		HardwareCompression: proto.Bool(false), EncryptionAlgorithm: proto.String("AES-256-GCM-128"),
	}, Reading: &entity.TapeReadTiming{BytesPerSecond: proto.Float64(140e6)}}
	value := &entity.DeviceSettings{LearnedReadParameters: []*entity.TapeLearnedReadParameters{{Device: "drive", Parameters: parameters}}}
	selected, source := selectTarReadParameters(value, "drive", report, &entity.TapeMediaProfile{Tar: profile})
	require.Equal(t, "learned", source)
	require.True(t, proto.Equal(parameters, selected))
	selected.Reading.BytesPerSecond = proto.Float64(1)
	require.Equal(t, 140e6, parameters.Reading.GetBytesPerSecond())

	// A different cartridge record envelope or unobserved firmware cannot reuse timings implicitly.
	profile.RecordBytes = 262144
	selected, source = selectTarReadParameters(value, "drive", report, &entity.TapeMediaProfile{Tar: profile})
	require.Equal(t, "builtin", source)
	require.Equal(t, uint32(262144), selected.AppliesTo.GetRecordBytes())
	require.Contains(t, selected.SourceReference, "builtin")
	profile.RecordBytes = 524288
	report.Firmware = nil
	selected, _ = selectTarReadParameters(value, "drive", report, &entity.TapeMediaProfile{Tar: profile})
	require.Nil(t, selected.AppliesTo.Firmware)
	require.NotEmpty(t, tarReadParametersReason(selected, report, &entity.TapeMediaProfile{Tar: profile}))
	report.Firmware = proto.String("firmware")
	selected, _ = selectTarReadParameters(value, "other", report, &entity.TapeMediaProfile{Tar: profile})
	require.Contains(t, selected.SourceReference, "builtin")

	// A deliberately partial manual draft remains partial and takes precedence over learned values.
	manual := &entity.TapeReadParameters{SourceReference: "operator partial input"}
	value.Devices = []*entity.DeviceConfig{{Device: "drive", Options: &entity.DeviceConfig_Tape{
		Tape: &entity.TapeDeviceSettings{ReadParameters: manual},
	}}}
	selected, source = selectTarReadParameters(value, "drive", report, &entity.TapeMediaProfile{Tar: profile})
	require.Equal(t, "manual", source)
	require.True(t, proto.Equal(manual, selected))
	require.Nil(t, selected.Reading)
}

func TestTarLearnedParametersRejectUnknownEncryptedApplicability(t *testing.T) {
	// A cartridge with no observed algorithm cannot reuse a model learned under a known algorithm.
	parameters, report, tar := tarReadParametersFixture()
	tar.EncryptionAlgorithm = nil
	profile := &entity.TapeMediaProfile{Tar: tar, Encryption: "synthetic saved key"}
	value := &entity.DeviceSettings{LearnedReadParameters: []*entity.TapeLearnedReadParameters{{Device: "drive", Parameters: parameters}}}
	selected, source := selectTarReadParameters(value, "drive", report, profile)
	require.Equal(t, "builtin", source)
	require.NotNil(t, selected)
	require.Nil(t, selected.AppliesTo.EncryptionAlgorithm)
	require.Contains(t, tarReadParametersReason(selected, report, profile), "encryption algorithm applicability is unknown")
	require.Equal(t, "AES-256-GCM-128", parameters.AppliesTo.GetEncryptionAlgorithm(), "the stored model stays unchanged")

	// An observed algorithm selects only learned read parameters recorded under that same condition.
	profile.Tar.EncryptionAlgorithm = proto.String("AES-256-GCM-128")
	selected, source = selectTarReadParameters(value, "drive", report, profile)
	require.Equal(t, "learned", source)
	require.Empty(t, tarReadParametersReason(selected, report, profile))
	require.Equal(t, "AES-256-GCM-128", selected.AppliesTo.GetEncryptionAlgorithm())
}
