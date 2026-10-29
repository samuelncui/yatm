package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"syscall"
	"time"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/protobuf/proto"
)

type CapacityQuality uint8

const (
	CapacityUnknown CapacityQuality = iota
	CapacityEstimate
	CapacityLowerBound
)

// CapacitySample describes recorded-EOD-to-EW native bytes, never head position or nominal capacity.
// Missing remaining bytes or allowances are unknown; they cannot establish a bounded fit guarantee.
type CapacitySample struct {
	Partition                    uint32
	IssuedAt, CompletedAt        time.Time
	RemainingBytes               *uint64
	Quality                      CapacityQuality
	ErrorBytes, UnreflectedBytes *uint64
}

// Capacity owns an attempt's optional query. Initial is immutable; the writer owns its cache.
type Capacity struct {
	Initial   *CapacitySample
	transport Transport
	partition uint32
}

// All capacity queries use the LTFS LTO LOG SENSE native timeout.
const capacityObservationTimeout = 60 * time.Second

func capacityCommand() Command {
	return Command{CDB: []byte{0x4d, 0, 0x4c, 0, 0, 0, 0, 0x10, 0, 0}, Direction: FromDevice,
		Data: make([]byte, MaxQueryBytes), Timeout: capacityObservationTimeout}
}

// capacityQueryError records a matched, settled read-only timeout or busy response. It says
// nothing about WRITE completion and never replaces another native or cleanup failure.
type capacityQueryError struct {
	response Response
	cause    error
}

func (e *capacityQueryError) Error() string {
	return fmt.Sprintf("capacity query failed, status=%02x host=%x driver=%x: %v",
		e.response.Status, e.response.HostStatus, e.response.DriverStatus, e.cause)
}

func (e *capacityQueryError) Unwrap() error { return e.cause }

func checkCommandResponse(command Command, response Response) error {
	// Only the exact optional counter query can treat a clean completed timeout/busy as a miss.
	// CheckResponse stays strict for WRITE, controls, and ambiguous native response envelopes.
	if !exactCapacityCommand(command) || !response.Completed || response.MessageStatus != 0 ||
		len(response.Sense) != 0 || response.Residual < 0 || int(response.Residual) > len(command.Data) {
		return CheckResponse(response)
	}
	switch {
	case response.Status == 0 && (response.HostStatus == 0 || response.HostStatus == 3) &&
		(response.DriverStatus == 0 || response.DriverStatus == 6) && (response.HostStatus == 3 || response.DriverStatus == 6):
		return &capacityQueryError{response: response, cause: context.DeadlineExceeded}
	case response.Status == 0 && (response.HostStatus == 0 || response.HostStatus == 2) &&
		(response.DriverStatus == 0 || response.DriverStatus == 1) && (response.HostStatus == 2 || response.DriverStatus == 1):
		return &capacityQueryError{response: response, cause: syscall.EBUSY}
	case response.Status == 0x08 && response.HostStatus == 0 && response.DriverStatus == 0:
		return &capacityQueryError{response: response, cause: syscall.EBUSY}
	}
	return CheckResponse(response)
}

func onlyCapacityQueryError(err error) bool {
	// Every joined cause must be query-local; a miss cannot hide a reset or loss.
	if _, ok := err.(*capacityQueryError); ok {
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return onlyCapacityQueryError(wrapped.Unwrap())
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !onlyCapacityQueryError(cause) {
				return false
			}
		}
		return true
	}
	return false
}

func capacitySubmissionRejected(err error) bool {
	// The native exchange wraps an errno. A joined cleanup/device failure is never optional.
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return capacitySubmissionRejected(wrapped.Unwrap())
	}
	return err == syscall.EINVAL || err == syscall.ENOTTY || err == syscall.EACCES || err == syscall.EPERM
}

// Observe never waits for lane admission; Linux writer transports own a private capacity lane.
// Its callback runs immediately before an issued query. Busy returns nil,false,nil;
// cancellation after issue still waits for native command settlement.
func (c *Capacity) Observe(ctx context.Context, onIssue func() error) (*CapacitySample, bool, error) {
	// An opaque transport cannot promise the issue boundary or nonqueued optional admission.
	transport, ok := c.transport.(interface {
		TryExecute(context.Context, Command, func() error) (Response, bool, error)
	})
	if !ok {
		return nil, false, ErrUnsupported
	}
	command := capacityCommand()
	var issuedAt time.Time
	response, issued, err := transport.TryExecute(ctx, command, func() error {
		if onIssue != nil {
			if err := onIssue(); err != nil {
				return err
			}
		}
		issuedAt = time.Now()
		return nil
	})
	if err != nil || !issued {
		return nil, issued, err
	}

	return capacityResponse(command, response, c.partition, issuedAt, time.Now())
}

func preparationCapacity(ctx context.Context, transport Transport, partition uint32) (*CapacitySample, bool, error) {
	// Exclusive preparation has no competing data; the interval conservatively includes Execute admission.
	command := capacityCommand()
	started := time.Now()
	response, err := transport.Execute(ctx, command)
	if err != nil {
		return nil, response.Completed, err
	}
	return capacityResponse(command, response, partition, started, time.Now())
}

func capacityResponse(command Command, response Response, partition uint32, started, completed time.Time) (*CapacitySample, bool, error) {
	// Host/sense/residual evidence remains authoritative before interpreting an optional value.
	if err := checkCommandResponse(command, response); err != nil {
		return nil, true, err
	}
	if response.Residual < 0 || int(response.Residual) > len(command.Data) || started.IsZero() {
		return nil, true, &UncertainError{Cause: ErrMalformed}
	}
	remaining, complete, err := decodeCapacity(command.Data[:len(command.Data)-int(response.Residual)])
	if err != nil {
		return nil, true, err
	}
	quality := CapacityUnknown
	if complete {
		quality = CapacityEstimate
	}
	return &CapacitySample{Partition: partition, IssuedAt: started, CompletedAt: completed,
		RemainingBytes: remaining, Quality: quality}, true, nil
}

func decodeCapacity(data []byte) (*uint64, bool, error) {
	// SSC-3 rev03e §8.2.2 and IBM GA32-0928-08 §6.4.8.1 define optional current-partition fields.
	// HP's documented four-byte layout is accepted; unknown/new layouts cannot invent an estimate.
	if len(data) < 4 || data[0]&0x7f != 0x0c || data[1] != 0 {
		return nil, false, ErrMalformed
	}
	end := 4 + int(binary.BigEndian.Uint16(data[2:4]))
	if end > len(data) {
		return nil, false, ErrMalformed
	}
	var eod, warning *uint32
	for offset := 4; offset < end; {
		if end-offset < 4 {
			return nil, false, ErrMalformed
		}
		code, size := binary.BigEndian.Uint16(data[offset:offset+2]), int(data[offset+3])
		if offset+4+size > end {
			return nil, false, ErrMalformed
		}
		if code == 4 || code == 5 {
			format := data[offset+2] & 3
			if size != 4 || format != 0 && format != 3 {
				return nil, false, ErrMalformed
			}
			value := binary.BigEndian.Uint32(data[offset+4 : offset+8])
			field := &eod
			if code == 5 {
				field = &warning
			}
			if *field != nil {
				return nil, false, ErrMalformed
			}
			*field = &value
		}
		offset += 4 + size
	}
	if eod == nil || warning == nil {
		return nil, false, nil
	}
	if *eod == math.MaxUint32 || *warning == math.MaxUint32 {
		return nil, true, nil
	}
	remaining := uint64(0)
	if *warning > *eod {
		remaining = uint64(*warning-*eod) * 1_000_000
	}
	return &remaining, true, nil
}

// Capacity obtains a single preparation observation after the configured baseline proves partition zero.
// Callers choose periodic observation cadence independently of preparation.
func (d *Device) Capacity(ctx context.Context) (*Capacity, error) {
	if d.prepared == nil || d.records.Mode.MaximumBytes == 0 || d.report.GetTape().GetPartitionCount() != 1 {
		return nil, fmt.Errorf("capacity observation requires a prepared single-partition Tape")
	}
	capacity := &Capacity{transport: d.transport}
	sample, issued, err := preparationCapacity(ctx, d.transport, 0)
	err = errors.Join(err, ctx.Err())
	d.reportCapacity(sample, issued, err)
	if errors.Is(err, ErrUncertain) || ctx.Err() != nil || Reason(err) == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || Reason(err) == entity.DeviceReason_DEVICE_REASON_NOT_READY {
		d.invalidateGeometry()
		return nil, err
	}
	capacity.Initial = sample
	return capacity, nil
}

func (d *Device) reportCapacity(sample *CapacitySample, issued bool, err error) {
	// Optional estimates never self-certify physical bounds or streaming qualification.
	f := feature(d.report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING)
	f.Validation = entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED
	f.Evidence = entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_UNSPECIFIED
	f.Limits = nil
	f.Support, f.Availability, f.Reason = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNKNOWN, entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN, entity.DeviceReason_DEVICE_REASON_NOT_READY
	f.Detail = "Capacity estimate unavailable"
	if err != nil {
		f.Reason, f.Detail = Reason(err), err.Error()
		if f.Reason == entity.DeviceReason_DEVICE_REASON_UNSUPPORTED {
			f.Support = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_UNSUPPORTED
			f.Availability = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE
		}
		if errors.Is(err, ErrUncertain) || f.Reason == entity.DeviceReason_DEVICE_REASON_NO_MEDIUM || f.Reason == entity.DeviceReason_DEVICE_REASON_NOT_READY {
			f.Availability = entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE
		}
		return
	}
	if !issued || sample == nil {
		return
	}
	f.Evidence = entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_REPORTED
	f.Support = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_PARTIAL
	limits := &entity.TapeCapacityLimits{Partition: proto.Uint32(sample.Partition), RemainingBytes: sample.RemainingBytes,
		IssuedAtNs: proto.Int64(sample.IssuedAt.UnixNano()), CompletedAtNs: proto.Int64(sample.CompletedAt.UnixNano())}
	if sample.Quality == CapacityEstimate {
		limits.Quality = entity.TapeCapacityQuality_TAPE_CAPACITY_QUALITY_ESTIMATE
		f.Support = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED
	}
	f.Limits = &entity.DeviceFeature_Capacity{Capacity: limits}
	if sample.RemainingBytes != nil {
		f.Support, f.Availability, f.Reason = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED, entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_READY, 0
		f.Detail = "Recorded-EOD-to-EW estimate observed; error and unreflected-write allowances unknown"
	}
}
