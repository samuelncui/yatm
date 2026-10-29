package executor

import (
	"fmt"
	"strings"

	"github.com/samuelncui/yatm/entity"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	settingspkg "github.com/samuelncui/yatm/internal/settings"
)

func tapeReadCompletion(parameters *entity.TapeReadParameters) *mediapkg.ReadCompletion {
	value := parameters.GetCompletion()
	if value == nil || value.FullSpanSeconds == nil || value.FixedSeconds == nil {
		return nil
	}
	return &mediapkg.ReadCompletion{FullSpanSeconds: value.GetFullSpanSeconds(), FixedSeconds: value.GetFixedSeconds()}
}

// tarReadParametersReason checks observed drive and frozen cartridge conditions before using estimates.
// A saved partial measurement remains useful input, but cannot label unknown conditions as a match.
func tarReadParametersReason(parameters *entity.TapeReadParameters, report *entity.InspectDeviceResponse,
	profile *entity.TapeMediaProfile,
) string {
	if report == nil || report.GetTape() == nil {
		return "Drive applicability observations are unavailable"
	}
	return tapeReadParametersReason(parameters, observedTarReadApplicability(report, profile))
}

func observedTarReadApplicability(report *entity.InspectDeviceResponse, profile *entity.TapeMediaProfile) *entity.TapeReadApplicability {
	if report.GetTape() == nil || profile.GetTar() == nil {
		return nil
	}
	// A cartridge with no observed algorithm stays unknown.
	tar := profile.GetTar()
	return &entity.TapeReadApplicability{DriveSerial: report.SerialNumber, Firmware: report.Firmware,
		DensityCode: report.GetTape().DensityCode, RecordMode: tar.RecordMode.Enum(), RecordBytes: &tar.RecordBytes,
		HardwareCompression: &tar.HardwareCompression, EncryptionAlgorithm: tar.EncryptionAlgorithm}
}

// tapeReadParametersReason compares observed conditions without manufacturing a format profile.
func tapeReadParametersReason(parameters *entity.TapeReadParameters, actual *entity.TapeReadApplicability) string {
	if parameters == nil {
		return "Read parameters are not configured"
	}
	if parameters.AppliesTo == nil || actual == nil {
		return "Read parameter applicability is incomplete"
	}
	scope := parameters.AppliesTo
	for _, condition := range []struct {
		name         string
		known, equal bool
	}{
		{"drive serial", scope.DriveSerial != nil && strings.TrimSpace(scope.GetDriveSerial()) != "" &&
			actual.DriveSerial != nil && strings.TrimSpace(actual.GetDriveSerial()) != "", scope.GetDriveSerial() == actual.GetDriveSerial()},
		{"firmware", scope.Firmware != nil && strings.TrimSpace(scope.GetFirmware()) != "" &&
			actual.Firmware != nil && strings.TrimSpace(actual.GetFirmware()) != "", scope.GetFirmware() == actual.GetFirmware()},
		{"density", scope.DensityCode != nil && actual.DensityCode != nil, scope.GetDensityCode() == actual.GetDensityCode()},
		{"record mode", scope.RecordMode != nil && actual.RecordMode != nil, scope.GetRecordMode() == actual.GetRecordMode()},
		{"record bytes", scope.RecordBytes != nil && actual.RecordBytes != nil, scope.GetRecordBytes() == actual.GetRecordBytes()},
		{"written compression", scope.HardwareCompression != nil && actual.HardwareCompression != nil, scope.GetHardwareCompression() == actual.GetHardwareCompression()},
		{"encryption algorithm", scope.EncryptionAlgorithm != nil && actual.EncryptionAlgorithm != nil, scope.GetEncryptionAlgorithm() == actual.GetEncryptionAlgorithm()},
	} {
		if !condition.known {
			return fmt.Sprintf("Read parameter %s applicability is unknown", condition.name)
		}
		if !condition.equal {
			return fmt.Sprintf("Read parameter %s applicability differs from the prepared Media", condition.name)
		}
	}
	return ""
}

func compileTarPositionMap(parameters *entity.TapeReadParameters, profile *entity.TarMediaProfile,
	value *entity.TapePositionMap, valid func() bool,
) (*mediapkg.ReadCostModel, string) {
	// Applicability is checked by the factory; incomplete optional inputs never deny ordinary reads.
	if parameters == nil || profile == nil || profile.HardwareCompression {
		return nil, "Uncompressed written conditions are required for linear interpolation"
	}
	return compileTapePositionMap(parameters, value, valid)
}

func compileTapePositionMap(parameters *entity.TapeReadParameters, value *entity.TapePositionMap,
	valid func() bool,
) (*mediapkg.ReadCostModel, string) {
	if err := settingspkg.ValidateTapeReadParameters(parameters); err != nil {
		return nil, err.Error()
	}
	positioning := parameters.GetPositioning()
	if positioning == nil || positioning.BaseSeconds == nil || positioning.WrapChangeSeconds == nil ||
		positioning.BandChangeSeconds == nil || positioning.LandingZoneChangeSeconds == nil ||
		positioning.DirectionChangeSeconds == nil || positioning.StepBackSeconds == nil ||
		positioning.LongitudinalSecondsPerWrap == nil && positioning.LongitudinalSecondsPerLpos == nil {
		return nil, "Positioning parameters are incomplete"
	}
	if value == nil || valid == nil || !valid() {
		return nil, "A current owned Tape position map is unavailable"
	}
	if positioning.LongitudinalSecondsPerWrap == nil && (value.MinLpos == nil || value.MaxLpos == nil) {
		return nil, "Absolute positioning requires explicit LPOS bounds"
	}

	// Persisted traversal supplies spatial facts only; current Device parameters supply every elapsed-time term.
	wraps := make([]mediapkg.ReadModelWrap, len(value.Wraps))
	for i, wrap := range value.Wraps {
		if wrap == nil {
			return nil, "Tape position map has a missing wrap"
		}
		if wrap.Endpoint != nil && wrap.EndpointUncertaintyObjects == nil {
			return nil, "Tape position map endpoint uncertainty is unknown"
		}
		wraps[i] = mediapkg.ReadModelWrap{Partition: wrap.Partition, Wrap: wrap.Wrap, Band: wrap.Band,
			Direction: mediapkg.ReadWrapDirection(wrap.Direction), Full: wrap.Full, Endpoint: wrap.Endpoint,
			EndpointUncertaintyObjects: wrap.EndpointUncertaintyObjects}
		if positioning.LongitudinalSecondsPerWrap != nil {
			wraps[i].EstimatedSpanObjects = wrap.EstimatedSpanObjects
		}
	}
	bot := make(map[uint32]uint64, len(value.Partitions))
	for _, partition := range value.Partitions {
		if partition == nil {
			return nil, "Tape position map has a missing partition"
		}
		bot[partition.Partition] = partition.Bot
	}
	model, err := mediapkg.NewReadCostModel(mediapkg.ReadModelProfile{
		ID: "tape:" + value.Source, SnapshotValid: valid,
		Boundary: mediapkg.ReadEndpointConvention(value.EndpointConvention),
		BOT:      bot, Wraps: wraps, MinLPOS: value.GetMinLpos(), MaxLPOS: value.GetMaxLpos(),
		FullWrapSeconds: positioning.LongitudinalSecondsPerWrap,
		Coefficients: mediapkg.ReadModelCoefficients{Base: positioning.GetBaseSeconds(), Wrap: positioning.GetWrapChangeSeconds(),
			Band: positioning.GetBandChangeSeconds(), LandingZone: positioning.GetLandingZoneChangeSeconds(),
			Direction: positioning.GetDirectionChangeSeconds(), StepBack: positioning.GetStepBackSeconds(),
			Longitudinal: positioning.GetLongitudinalSecondsPerLpos()},
	})
	if err != nil {
		return nil, err.Error()
	}
	if !model.Valid() {
		return nil, "Reported wrap intervals do not provide usable positioning coverage"
	}
	return model, "Uncompressed four-band linear interpolation"
}
