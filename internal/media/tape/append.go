package tape

import (
	"context"
	"fmt"
	"math"
)

// VerifyAppendTail proves the terminal filemark and restores the exact post-mark write position.
// The caller must first validate its archive label under this Device's read configuration.
// SSC makes SPACE to EOD mandatory; a rejected command, missing long position evidence, failed
// motion or a contradictory tail returns an error and never authorizes APPEND.
func (d *Device) VerifyAppendTail(ctx context.Context) (_ uint64, err error) {
	// Require the prepared long-position/LOCATE pair, because only long form reports file ordinals.
	defer func() { d.records.geometryError(err) }()
	if d.operation != Read {
		return 0, fmt.Errorf("APPEND tail verification requires read preparation")
	}
	navigation, err := d.Navigation(ctx)
	if err != nil {
		return 0, err
	}
	if navigation == nil || !navigation.long {
		return 0, fmt.Errorf("APPEND requires long READ POSITION and LOCATE support: %w", ErrUnsupported)
	}

	// SPACE locates recorded EOD but does not by itself prove the preceding file is terminated.
	if err := d.records.Space(ctx, 3, 0); err != nil {
		return 0, fmt.Errorf("position APPEND at recorded EOD failed, %w", err)
	}
	end, err := navigation.Position(ctx)
	if err != nil {
		return 0, fmt.Errorf("observe APPEND EOD position failed, %w", err)
	}
	if end.Partition != 0 || end.ObjectKnown && navigation.bits == 32 && end.Object > math.MaxUint32 {
		return 0, fmt.Errorf("APPEND EOD position is outside the qualified navigation range: %w", ErrMalformed)
	}
	if !end.ObjectKnown || !end.FileKnown {
		return 0, fmt.Errorf("APPEND EOD position does not report object and file counters: %w", ErrMalformed)
	}
	if end.Object == 0 || end.File == 0 || end.File == math.MaxUint64 || end.Object == math.MaxUint64 {
		return 0, fmt.Errorf("APPEND EOD counters cannot establish a following archive: %w", ErrMalformed)
	}

	// Verify the exact preceding object and file ordinal before checking its boundary response.
	if err := navigation.Locate(ctx, 0, end.Object-1); err != nil {
		return 0, fmt.Errorf("locate APPEND terminating filemark failed, %w", err)
	}
	before, err := navigation.Position(ctx)
	if err != nil {
		return 0, fmt.Errorf("verify APPEND terminating filemark position failed, %w", err)
	}
	if !appendPositionMatches(before, end.Object-1, end.File-1) {
		return 0, fmt.Errorf("APPEND terminating filemark position contradicts EOD: %w", ErrMalformed)
	}
	buffer := make([]byte, d.records.Mode.MaximumBytes)
	mark, err := d.records.Read(ctx, buffer)
	if err != nil {
		return 0, fmt.Errorf("read APPEND terminating filemark failed, %w", err)
	}
	if !mark.AcceptedKnown || mark.Bytes != 0 || !mark.Filemark || mark.EOD || mark.Sense.EOM || mark.Sense.ILI {
		return 0, fmt.Errorf("APPEND requires a proven terminating filemark without trailing data: %w", ErrMalformed)
	}
	tail, err := d.records.Read(ctx, buffer)
	if err != nil {
		return 0, fmt.Errorf("read APPEND recorded EOD failed, %w", err)
	}
	if !tail.AcceptedKnown || tail.Bytes != 0 || !tail.EOD || tail.Filemark || tail.Sense.Filemark || tail.Sense.ILI {
		return 0, fmt.Errorf("APPEND requires recorded EOD immediately after the terminating filemark: %w",
			ErrMalformed)
	}

	// Reading EOD may move the host cursor; restore and reobserve the original post-mark point.
	if err := navigation.Locate(ctx, 0, end.Object); err != nil {
		return 0, fmt.Errorf("restore proven APPEND position failed, %w", err)
	}
	restored, err := navigation.Position(ctx)
	if err != nil {
		return 0, fmt.Errorf("verify restored APPEND position failed, %w", err)
	}
	if !appendPositionMatches(restored, end.Object, end.File) {
		return 0, fmt.Errorf("restored APPEND position contradicts proven EOD: %w", ErrMalformed)
	}
	return end.File, nil
}

func appendPositionMatches(position Position, object, file uint64) bool {
	return position.Partition == 0 && position.ObjectKnown && position.FileKnown &&
		position.Object == object && position.File == file
}
