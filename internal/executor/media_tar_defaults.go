package executor

import (
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

const (
	tarBuiltinIBMReference = "https://www.ibm.com/support/pages/system/files/inline-files/" +
		"%5BAPPROVED%5D%20LTO%20SCSI%20Reference%20GA32-0928-06.pdf"
	tarBuiltinHP3Reference       = "https://docs.oracle.com/cd/E21407_01/en/CRCM2158/CRCM2158.pdf"
	tarBuiltinHP6Reference       = "https://docs.oracle.com/cd/E38452_01/en/LTO6_Vol4_E1/LTO6_Vol4_E1.pdf"
	tarBuiltinHP5Reference       = "https://docs.oracle.com/cd/E21419_04/en/LTO5_Vol4_E4/LTO5_Vol4_E4.pdf"
	tarBuiltinQuantumReference   = "https://cdn.allbound.com/iq-ab/2022/12/LTO-9-TB00068A.pdf"
	tarBuiltinEmpiricalReference = "docs/research/tape-read-ordering.md#uncompressed-full-cartridge-measurement"
)

func builtinTapeReadParameters(report *entity.InspectDeviceResponse, scope *entity.TapeReadApplicability) *entity.TapeReadParameters {
	if report.GetTape() == nil || scope == nil || scope.HardwareCompression == nil || scope.GetHardwareCompression() ||
		scope.GetRecordMode() == entity.TapeRecordMode_TAPE_RECORD_MODE_UNSPECIFIED || scope.GetRecordBytes() == 0 {
		return nil
	}
	// Bind this disposable prior to observed conditions without retaining request/shared-object pointers.
	applies := proto.Clone(scope).(*entity.TapeReadApplicability)
	parameters := &entity.TapeReadParameters{AppliesTo: applies,
		// These terms come from one HP5/Z6ED experiment, transferred as a prior.
		// Rewind specifications are independent mechanical references, not seven-term LOCATE fits.
		Positioning: &entity.TapeReadPositioning{
			BaseSeconds: proto.Float64(3.223942631933043), WrapChangeSeconds: proto.Float64(6.252332019046645),
			BandChangeSeconds: proto.Float64(-0.8279789490339896), LandingZoneChangeSeconds: proto.Float64(0.6469527733076674),
			DirectionChangeSeconds: proto.Float64(1.2685235350999264), StepBackSeconds: proto.Float64(5.965091877336342),
			LongitudinalSecondsPerWrap: proto.Float64(92.69041838827268),
		},
		Reading: &entity.TapeReadTiming{
			BytesPerSecond: proto.Float64(144180427.70319828), PostLocateReadOverheadSeconds: proto.Float64(0.011400702694972826),
		},
		// HP's provisional full-span rewind and <19s non-rewind unload references initialize this separate model.
		Completion: &entity.TapeReadCompletion{FullSpanSeconds: proto.Float64(96), FixedSeconds: proto.Float64(19)},
		SourceReference: "builtin unmeasured generic empirical HP5/Z6ED prior (2026-10-21); " +
			"transferred timing is not a device measurement; " + tarBuiltinEmpiricalReference +
			"; borrowed HP5 completion initialization (provisional density-specific full-span rewind, " +
			"<19s unload excluding rewind): " + tarBuiltinHP5Reference +
			"; recorded-density definitions: " + tarBuiltinIBMReference,
	}

	// Recorded density selects media layout, not the drive's model name or its newest supported generation.
	if applies.DensityCode == nil {
		parameters.SourceReference += "; recorded density absent; geometry and LPOS remain unknown"
		return parameters
	}
	wraps, rate, reference := tarBuiltinMediaDefaults(report.GetTape().GetDensityCode())
	// The same published reader lists distinct rewind references for its older supported media.
	if applies.GetDensityCode() == 0x44 {
		parameters.Completion.FullSpanSeconds = proto.Float64(78)
	} else if applies.GetDensityCode() == 0x46 {
		parameters.Completion.FullSpanSeconds = proto.Float64(93)
	}
	if wraps != 0 {
		parameters.Geometry = &entity.TapeReadGeometry{NominalWraps: proto.Uint32(wraps)}
	}
	if rate != 0 {
		parameters.Reading.BytesPerSecond = proto.Float64(rate)
	}
	parameters.SourceReference += "; " + reference
	return parameters
}

func tarBuiltinMediaDefaults(density uint32) (uint32, float64, string) {
	// HP Gen3 volume 4 pp20/25 supplies the first three layouts and its native reader-rate references.
	switch density {
	case 0x40:
		return 48, 20e6, "LTO-1 nominal 48 wraps; HP Gen3 LTO-1 rate reference; " + tarBuiltinHP3Reference
	case 0x42:
		return 64, 30e6, "LTO-2 nominal 64 wraps; HP Gen3 LTO-2 rate reference; " + tarBuiltinHP3Reference
	case 0x44:
		return 44, 80e6, "LTO-3 nominal 44 wraps; HP Gen3 rate reference; " + tarBuiltinHP3Reference
	case 0x46:
		return 56, 120e6, "LTO-4 nominal 56 wraps; HP Gen6 rate reference; " + tarBuiltinHP6Reference
	case 0x58:
		return 80, 140e6, "LTO-5 nominal 80 wraps; HP Gen6 rate reference; " + tarBuiltinHP6Reference
	case 0x5a:
		return 136, 160e6, "LTO-6 nominal 136 wraps; HP Gen6 rate reference; " + tarBuiltinHP6Reference
	case 0x5c:
		// IBM table 113 specifies 3584 recorded tracks / 32 simultaneous channels, hence 112 wraps.
		return 112, 300e6, "LTO-7 nominal 112 wraps (3584/32); Quantum rate reference; " + tarBuiltinQuantumReference
	case 0x5d:
		// IBM table 113 specifies 5376 recorded tracks / 32 simultaneous channels, hence 168 wraps.
		return 168, 300e6, "LTO-7 Type M nominal 168 wraps (5376/32); Quantum rate reference; " + tarBuiltinQuantumReference
	case 0x5e:
		return 208, 300e6, "LTO-8 nominal 208 wraps; Quantum half-height rate reference (full-height differs); " + tarBuiltinQuantumReference
	case 0x60:
		return 280, 300e6, "LTO-9 nominal 280 wraps; Quantum half-height rate reference (full-height differs); " + tarBuiltinQuantumReference
	default:
		return 0, 0, fmt.Sprintf("density 0x%x has no sourced layout; geometry/LPOS/filemark overhead remain unknown", density)
	}
}
