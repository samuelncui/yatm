package tape

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/samuelncui/yatm/entity"
)

const ltfsLabelMaximumBytes = 512 << 10

// ReadLTFSLabels reads the two physical partition prefixes before LTFS owns the device.
// Returned XML records are owned copies; semantic Label validation belongs to the consumer.
// The implementation cap is not an LTFS format maximum. Cleanup remains the caller's obligation.
// Native LOCATE/READ timeouts follow HP LTO-5 Technical Reference Manual volume 2, page 22.
func (d *Device) ReadLTFSLabels(ctx context.Context) ([2][]byte, error) {
	// Establish the bounded variable-record command envelope without changing drive controls or keys.
	var labels [2][]byte
	if err := ctx.Err(); err != nil {
		return labels, err
	}
	if d == nil || d.transport == nil || d.report.GetTape() == nil || d.operation != 0 {
		return labels, fmt.Errorf("LTFS Label observation requires an unconfigured opened device: %w", ErrUnsupported)
	}
	medium := d.report.GetTape()
	if medium.MediumPresent != nil && !medium.GetMediumPresent() ||
		d.report.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY || d.report.Reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM {
		return labels, fmt.Errorf("LTFS Label medium is not ready")
	}
	if medium.MediumPresent == nil {
		return labels, fmt.Errorf("LTFS Label medium presence is unknown: %w", ErrUnsupported)
	}
	limits := feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ).GetRecords()
	if limits == nil || limits.MinimumBytes == nil || limits.MaximumBytes == nil ||
		limits.GetMinimumBytes() > 80 || limits.GetMaximumBytes() < 80 ||
		limits.Variable != nil && !limits.GetVariable() {
		return labels, fmt.Errorf("LTFS Label record bounds are unavailable: %w", ErrUnsupported)
	}
	maximum := min(ltfsLabelMaximumBytes, int(limits.GetMaximumBytes()))
	if reserve, ok := d.transport.(interface {
		EnsureTransfer(context.Context, int) error
	}); ok {
		// EnsureTransfer owns reservation/readback; verify its exposed current bound when available.
		if err := reserve.EnsureTransfer(ctx, maximum); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
				errors.Is(err, ErrUncertain) {
				return labels, fmt.Errorf("reserve LTFS Label buffer failed, %w", errors.Join(err, ctx.Err()))
			}
			return labels, fmt.Errorf("LTFS Label host buffer is unavailable (%v): %w", err, ErrUnsupported)
		}
		if bounded, ok := d.transport.(interface{ TransferLimit() int }); ok && bounded.TransferLimit() < maximum {
			return labels, fmt.Errorf("LTFS Label buffer reservation readback is insufficient: %w", ErrUnsupported)
		}
	} else if bounded, ok := d.transport.(interface{ TransferLimit() int }); ok {
		maximum = min(maximum, bounded.TransferLimit())
	} else {
		return labels, fmt.Errorf("LTFS Label transport bound is unavailable: %w", ErrUnsupported)
	}
	if maximum < 80 {
		return labels, fmt.Errorf("LTFS Label transport buffer is insufficient: %w", ErrUnsupported)
	}
	buffer := make([]byte, maximum)
	records := Records{Transport: d.transport, Mode: RecordMode{MaximumBytes: maximum}, Timeout: 20 * time.Minute,
		invalidateGeometry: d.invalidateGeometry}

	// Qualify CP and physical partition one explicitly; the tar navigator remains partition-zero scoped.
	locate, _ := locateCDB(0x92, 1, 0)
	read, err := records.command(0x08, buffer, FromDevice)
	if err != nil {
		return labels, err
	}
	for _, form := range [][]byte{locate, read.CDB} {
		data, err := reportCommand(ctx, d.transport, form[0], nil)
		if err != nil {
			return labels, fmt.Errorf("observe LTFS Label command %02x failed, %w", form[0], err)
		}
		support, err := commandFormSupport(data, form)
		if err != nil || support != entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED {
			return labels, fmt.Errorf("LTFS Label command %02x form is unavailable: %w", form[0], ErrUnsupported)
		}
	}

	// Each prefix is VOL1, filemark, one complete XML record, filemark; filemarks count as objects.
	for partition := range labels {
		if err := records.Locate(ctx, byte(partition), 0); err != nil {
			return [2][]byte{}, fmt.Errorf("locate LTFS Label partition %d failed, %w", partition, err)
		}
		for step := range 4 {
			result, err := records.Read(ctx, buffer)
			if err != nil {
				if errors.Is(err, io.ErrShortBuffer) && result.Sense.ILI && result.Sense.InformationValid &&
					result.Sense.Information < 0 && onlyLTFSLabelOversize(err) {
					return [2][]byte{}, fmt.Errorf("LTFS Label record exceeds the observation bound: %w", ErrUnsupported)
				}
				return [2][]byte{}, fmt.Errorf("read LTFS Label partition %d record %d failed, %w", partition, step, err)
			}
			if !result.AcceptedKnown || result.EOD || step%2 == 1 && (!result.Filemark || result.Bytes != 0) ||
				step%2 == 0 && (result.Filemark || result.Bytes == 0) {
				return [2][]byte{}, fmt.Errorf("LTFS Label prefix framing is unavailable: %w", ErrUnsupported)
			}
			if step == 0 && (result.Bytes != 80 || !bytes.Equal(buffer[:4], []byte("VOL1")) ||
				!bytes.Equal(buffer[24:28], []byte("LTFS"))) {
				return [2][]byte{}, fmt.Errorf("LTFS VOL1 label is unavailable: %w", ErrUnsupported)
			}
			if step == 2 {
				labels[partition] = bytes.Clone(buffer[:result.Bytes])
			}
		}
	}
	return labels, nil
}

func onlyLTFSLabelOversize(err error) bool {
	if err == io.ErrShortBuffer {
		return true
	}
	if failure, ok := err.(*CommandError); ok {
		sense := failure.Sense
		return currentCommand(failure) && sense.Key == 0 && sense.ASC == 0 && sense.ASCQ == 0 &&
			sense.ILI && !sense.Filemark && !sense.EOM && sense.InformationValid && sense.Information < 0
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyLTFSLabelOversize(wrapped.Unwrap())
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyLTFSLabelOversize(cause) {
				return false
			}
		}
		return true
	}
	return false
}
