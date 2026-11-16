package executor

import (
	"slices"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

// Built-in positioning and reading terms come from the fixed calibration sequence on one HP LTO-5
// drive. Fixed and full-span completion come from tar tails; the LTFS unmount time is the mean LTFS
// tail beyond them. The research document lists the measurements and every layout reference.
const (
	tarSeedBaseSeconds             = 6.15
	tarSeedApproachReversalSeconds = 4.78
	tarSeedSeekSecondsPerWrap      = 93.3
	tarSeedStreamingBytes          = 888.8e6
	// The measured HP LTO-5 rate prices reads only on a density without a native rate in tarDensities.
	tarSeedBytesPerSecond         = 145.4e6
	tarSeedFullSpanSeconds        = 96
	tarSeedFixedCompletionSeconds = 19
	tarSeedLTFSUnmountSeconds     = 12.61
	tarBuiltinSourceReference     = "built-in (HP LTO-5 calibration sequence)"
)

// tarDensity is the nominal layout of one recorded density: wrap count, native capacity, native
// read rate and the full-span rewind time where a reference differs from the seed.
type tarDensity struct {
	wraps           uint32
	nativeBytes     float64
	bytesPerSecond  float64
	fullSpanSeconds float64
}

// tarDensities are keyed by the recorded density, not the drive model, because a newer drive reads
// older cartridges in their own layout.
var tarDensities = map[uint32]tarDensity{
	0x40: {wraps: 48, nativeBytes: 100e9, bytesPerSecond: 20e6},
	0x42: {wraps: 64, nativeBytes: 200e9, bytesPerSecond: 30e6},
	0x44: {wraps: 44, nativeBytes: 400e9, bytesPerSecond: 80e6, fullSpanSeconds: 78},
	0x46: {wraps: 56, nativeBytes: 800e9, bytesPerSecond: 120e6, fullSpanSeconds: 93},
	0x58: {wraps: 80, nativeBytes: 1500e9, bytesPerSecond: 140e6},
	0x5a: {wraps: 136, nativeBytes: 2500e9, bytesPerSecond: 160e6},
	0x5c: {wraps: 112, nativeBytes: 6000e9, bytesPerSecond: 300e6},
	0x5d: {wraps: 168, nativeBytes: 9000e9, bytesPerSecond: 300e6},
	0x5e: {wraps: 208, nativeBytes: 12000e9, bytesPerSecond: 300e6},
	0x60: {wraps: 280, nativeBytes: 18000e9, bytesPerSecond: 300e6},
	0x62: {wraps: 472, nativeBytes: 30000e9, bytesPerSecond: 400e6},
	0x63: {wraps: 472, nativeBytes: 40000e9, bytesPerSecond: 400e6},
}

// nominalWrapBytes is the native capacity of one wrap of the recorded density, or zero when the
// density is unreported or unknown.
func nominalWrapBytes(densityCode *uint32) float64 {
	density, known := tarDensities[densityCode32(densityCode)]
	if densityCode == nil || !known {
		return 0
	}
	return density.nativeBytes / float64(density.wraps)
}

func densityCode32(code *uint32) uint32 {
	if code == nil {
		return 0
	}
	return *code
}

// BuiltinTapeReadParameters returns the built-in set for every recorded density with a known layout,
// ordered by density code, as read planning would use it for a cartridge of that density.
func BuiltinTapeReadParameters() []*entity.TapeReadParameters {
	codes := make([]uint32, 0, len(tarDensities))
	for code := range tarDensities {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	values := make([]*entity.TapeReadParameters, 0, len(codes))
	for _, code := range codes {
		values = append(values, builtinTapeReadParametersFor(proto.Uint32(code)))
	}
	return values
}

// builtinTapeReadParameters binds the built-in values to the loaded cartridge's recorded density.
func builtinTapeReadParameters(report *entity.InspectDeviceResponse) *entity.TapeReadParameters {
	return builtinTapeReadParametersFor(report.GetTape().DensityCode)
}

// builtinTapeReadParametersFor returns the built-in values for one recorded density, which may be absent.
func builtinTapeReadParametersFor(densityCode *uint32) *entity.TapeReadParameters {
	applies := &entity.TapeReadApplicability{}
	if densityCode != nil {
		applies.DensityCode = proto.Uint32(*densityCode)
	}
	parameters := &entity.TapeReadParameters{AppliesTo: applies,
		Positioning: &entity.TapeReadPositioning{
			BaseSeconds: proto.Float64(tarSeedBaseSeconds), ApproachReversalSeconds: proto.Float64(tarSeedApproachReversalSeconds),
			SeekSecondsPerWrap: proto.Float64(tarSeedSeekSecondsPerWrap), StreamingBytes: proto.Uint64(tarSeedStreamingBytes),
		},
		Reading: &entity.TapeReadTiming{BytesPerSecond: proto.Float64(tarSeedBytesPerSecond)},
		Completion: &entity.TapeReadCompletion{FullSpanSeconds: proto.Float64(tarSeedFullSpanSeconds), FixedSeconds: proto.Float64(tarSeedFixedCompletionSeconds),
			LtfsUnmountSeconds: proto.Float64(tarSeedLTFSUnmountSeconds)},
		SourceReference: tarBuiltinSourceReference,
	}

	// An absent or unknown recorded density leaves geometry unknown instead of borrowing an LTO layout.
	density, known := tarDensities[applies.GetDensityCode()]
	if applies.DensityCode == nil || !known {
		return parameters
	}
	parameters.Geometry = &entity.TapeReadGeometry{NominalWraps: proto.Uint32(density.wraps)}
	parameters.Reading.BytesPerSecond = proto.Float64(density.bytesPerSecond)
	if density.fullSpanSeconds != 0 {
		parameters.Completion.FullSpanSeconds = proto.Float64(density.fullSpanSeconds)
	}
	return parameters
}
