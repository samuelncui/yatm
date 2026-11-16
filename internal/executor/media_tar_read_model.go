package executor

import (
	"fmt"
	"math"
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
	return &mediapkg.ReadCompletion{FullSpanSeconds: value.GetFullSpanSeconds(), FixedSeconds: value.GetFixedSeconds(),
		LTFSUnmountSeconds: value.GetLtfsUnmountSeconds()}
}

// selectTapeReadParameters chooses the Device's set for the loaded cartridge's recorded density,
// otherwise the built-in values. A set that names another drive serial does not apply; the returned
// reason then explains why the built-in values are used. Without a Tape report no set is available.
func selectTapeReadParameters(value *entity.DeviceSettings, device string, report *entity.InspectDeviceResponse,
) (*entity.TapeReadParameters, string, string) {
	if report.GetTape() == nil {
		return nil, "unavailable", "Drive applicability observations are unavailable"
	}
	reason := ""
	if density := report.GetTape().DensityCode; density != nil {
		for _, configured := range settingspkg.TapeDefaults(value, device).GetReadParameters() {
			if configured.GetAppliesTo().GetDensityCode() != *density {
				continue
			}
			reason = tapeReadParametersReason(configured, report)
			if reason == "" {
				// Explicit parameters retain their presence and unknown values.
				return configured, "configured", ""
			}
			break
		}
	}
	return builtinTapeReadParameters(report), "builtin", reason
}

// tapeReadParametersReason reports why a configured set for the loaded density does not apply to the
// inspected drive: it names another drive serial than the one the drive reports.
func tapeReadParametersReason(parameters *entity.TapeReadParameters, report *entity.InspectDeviceResponse) string {
	scope := parameters.GetAppliesTo()
	if scope.DriveSerial == nil || strings.TrimSpace(report.GetSerialNumber()) == scope.GetDriveSerial() {
		return ""
	}
	return fmt.Sprintf("Read parameters for density code %d apply to drive serial %q, not the reported %q; built-in values are used",
		scope.GetDensityCode(), scope.GetDriveSerial(), strings.TrimSpace(report.GetSerialNumber()))
}

func compileTarPositionMap(parameters *entity.TapeReadParameters, profile *entity.TarMediaProfile,
	value *tapePositionMap, valid func() bool,
) (*mediapkg.ReadCostModel, string) {
	// Linear interpolation needs the Media's uncompressed written records of a known size; incomplete
	// optional inputs never deny ordinary reads.
	if parameters == nil || profile == nil || profile.HardwareCompression ||
		profile.RecordMode == entity.TapeRecordMode_TAPE_RECORD_MODE_UNSPECIFIED || profile.RecordBytes == 0 {
		return nil, "Uncompressed written conditions with a known record size are required for linear interpolation"
	}
	return compileTapePositionMap(parameters, profile.RecordBytes, value, valid)
}

// compileTapePositionMap prices the Session's map with the parameter terms; recordBytes is the
// Session's actual record or block size.
func compileTapePositionMap(parameters *entity.TapeReadParameters, recordBytes uint32, value *tapePositionMap,
	valid func() bool,
) (*mediapkg.ReadCostModel, string) {
	positioning := parameters.GetPositioning()
	if positioning == nil || positioning.BaseSeconds == nil || positioning.ApproachReversalSeconds == nil ||
		positioning.SeekSecondsPerWrap == nil {
		return nil, "Positioning parameters are incomplete"
	}
	// Reading-speed moves are priced from the record size and read rate, not a positioning term.
	readObjectSeconds, known := tapeReadObjectSeconds(parameters, recordBytes)
	if !known {
		return nil, "Reading speed requires record bytes and a read rate"
	}
	if value == nil || valid == nil || !valid() {
		return nil, "A current owned Tape position map is unavailable"
	}

	// The Session's traversal supplies spatial facts only; current Device parameters supply every elapsed-time term.
	bot := make(map[uint32]uint64, len(value.eod))
	for partition := range value.eod {
		bot[partition] = 0
	}
	model, err := mediapkg.NewReadCostModel(mediapkg.ReadModelProfile{
		ID: "tape:" + value.source, SnapshotValid: valid,
		BOT: bot, Wraps: value.wraps, ReadObjectSeconds: readObjectSeconds,
		BufferObjects: tapeBufferObjects(parameters, recordBytes),
		// The built-in base time floors the minimum gain: a configured base of zero keeps it.
		MinimumGainSeconds: tarSeedBaseSeconds,
		Coefficients: mediapkg.ReadModelCoefficients{Base: positioning.GetBaseSeconds(),
			ApproachReversal: positioning.GetApproachReversalSeconds(), Seek: positioning.GetSeekSecondsPerWrap()},
	})
	if err != nil {
		return nil, err.Error()
	}
	if !model.Valid() {
		return nil, "Reported wrap intervals do not provide usable positioning coverage"
	}
	return model, "Uncompressed four-band linear interpolation"
}

// tapeBufferObjects converts the read-ahead buffer to whole logical objects of the Session's record
// size; an absent buffer or record size never applies.
func tapeBufferObjects(parameters *entity.TapeReadParameters, recordBytes uint32) uint64 {
	if recordBytes == 0 {
		return 0
	}
	return parameters.GetPositioning().GetBufferBytes() / uint64(recordBytes)
}

// tapeReadObjectSeconds is the time to pass one logical object at reading speed: the Session's
// record bytes divided by the read rate.
func tapeReadObjectSeconds(parameters *entity.TapeReadParameters, recordBytes uint32) (float64, bool) {
	rate := parameters.GetReading().GetBytesPerSecond()
	if recordBytes == 0 || rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0, false
	}
	return float64(recordBytes) / rate, true
}
