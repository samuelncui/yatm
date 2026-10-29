package executor

import (
	"context"
	"errors"
	"fmt"

	"github.com/samuelncui/yatm/entity"
)

// ResolveTapeReadMedia identifies an unresolved Tape by its cartridge memory barcode under the existing attempt lease.
// The runner callback alone selects frozen pending Media; this operation owns no Job database.
func (e *Executor) ResolveTapeReadMedia(
	ctx context.Context, jobID int64, target *entity.ReadTapeTarget,
	selectMedia func(context.Context, string) (*entity.ReadMediaTarget, error),
) (selected *entity.ReadMediaTarget, returnErr error) {
	// Reuse the attempt's drive hold and validate access before passive identification.
	if target == nil || selectMedia == nil {
		return nil, fmt.Errorf("Tape identification requires frozen candidate selection")
	}
	device := target.Device
	if err := e.AcquireTapeDevice(ctx, jobID, device, nil); err != nil {
		return nil, err
	}
	prepared, err := e.openRawTape(ctx, jobID, device)
	if err != nil {
		return nil, err
	}
	defer func() { returnErr = errors.Join(returnErr, e.cleanupRawTape(ctx, jobID, device, prepared)) }()

	// The cartridge-memory barcode is the Tape identity; a cartridge without one is unusable.
	value, err := prepared.Barcode(ctx)
	if err != nil {
		return nil, err
	}
	barcode, err := normalizeReportedTapeBarcode(value)
	if err != nil {
		return nil, err
	}
	if barcode == "" {
		return nil, fmt.Errorf("Tape identity is unavailable, device=%q", device)
	}
	return selectMedia(ctx, barcode)
}
