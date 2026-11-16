package executor

import (
	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

// Initial positioning, reading and completion terms, measured on HP LTO-5 and corrected by passive
// learning on each drive. The research document lists the measurement and every layout reference.
const (
	tarSeedBaseSeconds                   = 3.223942631933043
	tarSeedWrapChangeSeconds             = 6.252332019046645
	tarSeedBandChangeSeconds             = -0.8279789490339896
	tarSeedLandingZoneChangeSeconds      = 0.6469527733076674
	tarSeedDirectionChangeSeconds        = 1.2685235350999264
	tarSeedStepBackSeconds               = 5.965091877336342
	tarSeedLongitudinalSecondsPerWrap    = 92.69041838827268
	tarSeedBytesPerSecond                = 144180427.70319828
	tarSeedPostLocateReadOverheadSeconds = 0.011400702694972826
	tarSeedFullSpanSeconds               = 96
	tarSeedFixedCompletionSeconds        = 19
	tarBuiltinSourceReference            = "built-in (HP LTO-5 measurement)"
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
}

func builtinTapeReadParameters(report *entity.InspectDeviceResponse, scope *entity.TapeReadApplicability) *entity.TapeReadParameters {
	if report.GetTape() == nil || scope == nil || scope.HardwareCompression == nil || scope.GetHardwareCompression() ||
		scope.GetRecordMode() == entity.TapeRecordMode_TAPE_RECORD_MODE_UNSPECIFIED || scope.GetRecordBytes() == 0 {
		return nil
	}
	// Bind this disposable prior to observed conditions without retaining request/shared-object pointers.
	applies := proto.Clone(scope).(*entity.TapeReadApplicability)
	parameters := &entity.TapeReadParameters{AppliesTo: applies,
		Positioning: &entity.TapeReadPositioning{
			BaseSeconds: proto.Float64(tarSeedBaseSeconds), WrapChangeSeconds: proto.Float64(tarSeedWrapChangeSeconds),
			BandChangeSeconds: proto.Float64(tarSeedBandChangeSeconds), LandingZoneChangeSeconds: proto.Float64(tarSeedLandingZoneChangeSeconds),
			DirectionChangeSeconds: proto.Float64(tarSeedDirectionChangeSeconds), StepBackSeconds: proto.Float64(tarSeedStepBackSeconds),
			LongitudinalSecondsPerWrap: proto.Float64(tarSeedLongitudinalSecondsPerWrap),
		},
		Reading: &entity.TapeReadTiming{
			BytesPerSecond: proto.Float64(tarSeedBytesPerSecond), PostLocateReadOverheadSeconds: proto.Float64(tarSeedPostLocateReadOverheadSeconds),
		},
		Completion:      &entity.TapeReadCompletion{FullSpanSeconds: proto.Float64(tarSeedFullSpanSeconds), FixedSeconds: proto.Float64(tarSeedFixedCompletionSeconds)},
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
