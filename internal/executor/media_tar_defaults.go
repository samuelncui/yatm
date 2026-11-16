package executor

import (
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
	tarSeedBufferBytes             = 888.8e6
	// The measured HP LTO-5 rate prices reads only on a density without a native rate in tarDensities.
	tarSeedBytesPerSecond         = 145.4e6
	tarSeedFullSpanSeconds        = 96
	tarSeedFixedCompletionSeconds = 19
	tarSeedLTFSUnmountSeconds     = 12.61
	tarBuiltinSourceReference     = "built-in (HP LTO-5 calibration sequence)"
)

// tarDensity is the nominal layout of one recorded density: wrap count, native read rate and the
// full-span rewind time where a reference differs from the seed.
type tarDensity struct {
	wraps           uint32
	bytesPerSecond  float64
	fullSpanSeconds float64
}

// tarDensities are keyed by the recorded density, not the drive model, because a newer drive reads
// older cartridges in their own layout.
var tarDensities = map[uint32]tarDensity{
	0x40: {wraps: 48, bytesPerSecond: 20e6},
	0x42: {wraps: 64, bytesPerSecond: 30e6},
	0x44: {wraps: 44, bytesPerSecond: 80e6, fullSpanSeconds: 78},
	0x46: {wraps: 56, bytesPerSecond: 120e6, fullSpanSeconds: 93},
	0x58: {wraps: 80, bytesPerSecond: 140e6},
	0x5a: {wraps: 136, bytesPerSecond: 160e6},
	0x5c: {wraps: 112, bytesPerSecond: 300e6},
	0x5d: {wraps: 168, bytesPerSecond: 300e6},
	0x5e: {wraps: 208, bytesPerSecond: 300e6},
	0x60: {wraps: 280, bytesPerSecond: 300e6},
	0x62: {wraps: 472, bytesPerSecond: 400e6},
	0x63: {wraps: 472, bytesPerSecond: 400e6},
}

// builtinTapeReadParameters binds the built-in values to the loaded cartridge's recorded density.
func builtinTapeReadParameters(report *entity.InspectDeviceResponse) *entity.TapeReadParameters {
	applies := &entity.TapeReadApplicability{}
	if density := report.GetTape().DensityCode; density != nil {
		applies.DensityCode = proto.Uint32(*density)
	}
	parameters := &entity.TapeReadParameters{AppliesTo: applies,
		Positioning: &entity.TapeReadPositioning{
			BaseSeconds: proto.Float64(tarSeedBaseSeconds), ApproachReversalSeconds: proto.Float64(tarSeedApproachReversalSeconds),
			SeekSecondsPerWrap: proto.Float64(tarSeedSeekSecondsPerWrap), BufferBytes: proto.Uint64(tarSeedBufferBytes),
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
