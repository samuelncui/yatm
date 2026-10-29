package tape

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
)

func capacityPage(values ...uint32) []byte {
	data := make([]byte, 4+8*len(values))
	data[0] = 0x0c
	binary.BigEndian.PutUint16(data[2:4], uint16(len(data)-4))
	for i, value := range values {
		offset := 4 + i*8
		binary.BigEndian.PutUint16(data[offset:offset+2], uint16(i+4))
		data[offset+2], data[offset+3] = 0x60, 4
		binary.BigEndian.PutUint32(data[offset+4:offset+8], value)
	}
	return data
}

func TestCapacityUsesRecordedEODDecimalMBAndPreservesUnknown(t *testing.T) {
	for _, tc := range []struct {
		name     string
		values   []uint32
		complete bool
		known    bool
		bytes    uint64
	}{
		{"recorded EOD not head or reserve", []uint32{100, 200, 1000, 199, 50}, true, true, 100_000_000},
		{"beyond EW", []uint32{201, 200}, true, true, 0},
		{"zero distance", []uint32{0, 0}, true, true, 0},
		{"unknown EOD", []uint32{math.MaxUint32, 200}, true, false, 0},
		{"unknown EW", []uint32{100, math.MaxUint32}, true, false, 0},
		{"missing field", []uint32{100}, false, false, 0},
		{"missing both", nil, false, false, 0},
		{"wide multiplication", []uint32{0, math.MaxUint32 - 1}, true, true, uint64(math.MaxUint32-1) * 1_000_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Optional unknown fields never become zero, a head-distance sample or a lower bound.
			remaining, complete, err := decodeCapacity(capacityPage(tc.values...))
			if err != nil || complete != tc.complete || (remaining != nil) != tc.known || remaining != nil && *remaining != tc.bytes {
				t.Fatalf("remaining=%v complete=%t err=%v", remaining, complete, err)
			}
		})
	}
}

func TestCapacityRefusesMalformedOrInapplicableLayouts(t *testing.T) {
	for _, change := range []func([]byte) []byte{
		func(data []byte) []byte { return data[:3] },
		func(data []byte) []byte { data[0] = 0x31; return data },
		func(data []byte) []byte { data[0] |= 0x40; return data },
		func(data []byte) []byte { data[1] = 1; return data },
		func(data []byte) []byte { return data[:len(data)-1] },
		func(data []byte) []byte { data[7] = 8; return data },
		func(data []byte) []byte { data[6] = 1; return data },
		func(data []byte) []byte { data[13] = 4; return data },
	} {
		// Page/subpage, envelopes, exact field length/format and uniqueness are checked before arithmetic.
		if _, _, err := decodeCapacity(change(capacityPage(100, 200))); !errors.Is(err, ErrMalformed) {
			t.Fatalf("inapplicable capacity layout accepted: %v", err)
		}
	}
}

func TestCapacityAcceptsStandardBinaryAndDocumentedCounterFields(t *testing.T) {
	for _, format := range []byte{0, 3} {
		// SSC binary-list and documented HP counter envelopes carry the same four-byte native fields.
		data := capacityPage(100, 200)
		data[6], data[14] = 0x60|format, 0x60|format
		remaining, complete, err := decodeCapacity(data)
		if err != nil || !complete || remaining == nil || *remaining != 100_000_000 {
			t.Fatalf("format=%d remaining=%v complete=%t err=%v", format, remaining, complete, err)
		}
	}
}

func TestCapacityInspectionUsesExactPositionWhenModePartitionAbsent(t *testing.T) {
	for _, known := range []bool{false, true} {
		// The current-partition page carries no partition ID; the preceding passive exact observation owns it.
		queries := 0
		transport := newSerializedTransport(func(command Command) (Response, error) {
			response, err := inspectionFixture(command)
			if command.CDB[0] == 0x5a {
				binary.BigEndian.PutUint16(command.Data[:2], 38) // Omit page10 from the current-mode envelope.
			}
			if command.CDB[0] == 0x34 {
				binary.BigEndian.PutUint32(command.Data[4:8], 17)
				if !known {
					command.Data[0] |= 4
				}
			}
			if command.CDB[0] == 0x4d {
				queries++
			}
			return response, err
		}, func() error { return nil })
		defer transport.Close()
		report, err := inspectTransport(context.Background(), Identity{}, transport)
		if err != nil {
			t.Fatal(err)
		}
		limits := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING).GetCapacity()
		if known && (queries != 1 || limits.GetPartition() != 17) || !known && (queries != 0 || limits != nil) {
			t.Fatalf("known=%t queries=%d capacity=%v", known, queries, limits)
		}
	}
}

func TestOptionalCapacityRejectsMutationAndClosedOrCanceledAdmission(t *testing.T) {
	// Optional priority applies only to the bounded passive form, never a data/control command.
	calls := 0
	transport := newSerializedTransport(func(Command) (Response, error) { calls++; return Response{Completed: true}, nil }, func() error { return nil })
	command := capacityCommand()
	for _, change := range []struct {
		index int
		value byte
	}{{0, 0x4c}, {1, 1}, {2, 0x0c}, {2, 0x71}, {3, 1}, {5, 1}, {9, 1}} {
		altered := command
		altered.CDB = append([]byte(nil), command.CDB...)
		altered.CDB[change.index] = change.value
		if _, issued, err := transport.TryExecute(context.Background(), altered, nil); issued || err == nil || calls != 0 {
			t.Fatalf("unsafe optional command=%x issued=%t err=%v", altered.CDB, issued, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, issued, err := transport.TryExecute(ctx, command, func() error { t.Error("canceled callback ran"); return nil }); issued || !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("canceled issued=%t err=%v", issued, err)
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	if _, issued, err := transport.TryExecute(context.Background(), command, func() error { t.Error("closed callback ran"); return nil }); issued || !errors.Is(err, io.ErrClosedPipe) || calls != 0 {
		t.Fatalf("closed issued=%t err=%v", issued, err)
	}
}

func TestOptionalCapacityPollDoesNotQueueAheadOfData(t *testing.T) {
	// Keep the existing owner busy while an actual WRITE waits; optional polling must return immediately.
	entered, release, written := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	var submitted atomic.Bool
	var polls atomic.Int32
	transport := newSerializedTransport(func(command Command) (Response, error) {
		switch command.CDB[0] {
		case 0:
			close(entered)
			<-release
		case 0x4d:
			polls.Add(1)
		case 0x0a:
			if !submitted.Load() {
				t.Error("WRITE accounting did not precede actual issue")
			}
		}
		return Response{Completed: true}, nil
	}, func() error { return nil })
	defer transport.Close()
	active := make(chan error, 1)
	go func() {
		_, err := transport.Execute(context.Background(), Command{CDB: make([]byte, 6), Direction: NoData, Timeout: time.Second})
		active <- err
	}()
	<-entered
	go func() {
		records := Records{Transport: transport, Mode: RecordMode{MaximumBytes: 512}, Timeout: time.Second}
		_, err := records.WriteObserved(context.Background(), make([]byte, 512), func() error { submitted.Store(true); return nil })
		written <- err
	}()

	// Busy polls have neither a submission callback nor a queued command; WRITE progresses after release.
	capacity := &Capacity{transport: transport}
	for i := 0; i < 100; i++ {
		_, issued, err := capacity.Observe(context.Background(), func() error { t.Error("busy poll callback ran"); return nil })
		if issued || err != nil {
			t.Fatalf("busy issued=%t err=%v", issued, err)
		}
	}
	if submitted.Load() {
		t.Fatal("queued WRITE counted as issued")
	}
	close(release)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-written:
		if err != nil || polls.Load() != 0 {
			t.Fatalf("write=%v polls=%d", err, polls.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("unissued poll blocked data progress")
	}
}

func TestIssuedCapacityPollSettlesBeforeWriteAndClose(t *testing.T) {
	// An optional query that actually issued owns the same lane until it settles, even after cancellation.
	entered, release := make(chan struct{}), make(chan struct{})
	var callback, writes, closes atomic.Int32
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0x4d {
			if callback.Load() != 1 {
				t.Error("capacity query issued before its accounting callback")
			}
			close(entered)
			<-release
			return inspectionFixture(command)
		}
		writes.Add(1)
		return Response{Completed: true}, nil
	}, func() error { closes.Add(1); return nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := make(chan error, 1)
	capacity := &Capacity{transport: transport, partition: 2}
	go func() {
		_, issued, err := capacity.Observe(ctx, func() error { callback.Add(1); return nil })
		if !issued {
			t.Error("active query marked unissued")
		}
		observed <- err
	}()
	<-entered
	cancel()
	writeCtx, stopWrite := context.WithCancel(context.Background())
	defer stopWrite()
	written := make(chan error, 1)
	go func() {
		_, err := (Records{Transport: transport, Mode: RecordMode{MaximumBytes: 512}, Timeout: time.Second}).Write(writeCtx, make([]byte, 512))
		written <- err
	}()
	closed := make(chan error, 1)
	go func() { closed <- transport.Close() }()

	// Cancellation cannot close the descriptor or allow a concurrent WRITE beside the active poll.
	select {
	case <-observed:
		t.Fatal("query returned before settlement")
	case <-time.After(20 * time.Millisecond):
	}
	if writes.Load() != 0 || closes.Load() != 0 {
		t.Fatal("active poll overlapped write/close")
	}
	stopWrite()
	close(release)
	if err := <-observed; !errors.Is(err, context.Canceled) {
		t.Fatalf("settled cancellation=%v", err)
	}
	if err := <-written; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued write=%v", err)
	}
	if err := <-closed; err != nil || closes.Load() != 1 {
		t.Fatalf("close=%v count=%d", err, closes.Load())
	}
}

func TestIssuedCapacitySamplePrecedesActualQueuedWrite(t *testing.T) {
	// Once issued, the observation's interval ends before the next WRITE accounting/command starts.
	entered, release := make(chan struct{}), make(chan struct{})
	var phase atomic.Int32
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0x4d {
			if phase.Load() != 1 {
				t.Error("query did not begin at its issue callback")
			}
			close(entered)
			<-release
			phase.Store(2)
			return inspectionFixture(command)
		}
		if phase.Load() != 3 {
			t.Error("WRITE interleaved with the issued query or missed its callback")
		}
		return Response{Completed: true}, nil
	}, func() error { return nil })
	defer transport.Close()
	observed := make(chan *CapacitySample, 1)
	go func() {
		sample, issued, err := (&Capacity{transport: transport, partition: 7}).Observe(context.Background(), func() error { phase.Store(1); return nil })
		if !issued || err != nil {
			t.Errorf("issued=%t err=%v", issued, err)
		}
		observed <- sample
	}()
	<-entered
	written := make(chan error, 1)
	go func() {
		_, err := (Records{Transport: transport, Mode: RecordMode{MaximumBytes: 512}, Timeout: time.Second}).WriteObserved(context.Background(), make([]byte, 512), func() error {
			if phase.Load() != 2 {
				t.Error("queued WRITE was counted before query settlement")
			}
			phase.Store(3)
			return nil
		})
		written <- err
	}()

	// Releasing the native command produces an estimate and allows the queued record to progress.
	close(release)
	sample := <-observed
	if sample == nil || sample.Partition != 7 || sample.Quality != CapacityEstimate || sample.RemainingBytes == nil ||
		*sample.RemainingBytes != 100_000_000 || sample.ErrorBytes != nil || sample.UnreflectedBytes != nil || sample.CompletedAt.Before(sample.IssuedAt) {
		t.Fatalf("sample=%+v", sample)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
}

func TestCapacityInspectionUncertaintyRemainsAttemptWide(t *testing.T) {
	// Optional semantic absence is local, but an actual unknown command/reset cannot continue preparation.
	closes := 0
	transport := newSerializedTransport(func(command Command) (Response, error) {
		if command.CDB[0] == 0x4d {
			return Response{Completed: true, HostStatus: 8}, nil // Linux DID_RESET.
		}
		return inspectionFixture(command)
	}, func() error { closes++; return nil })
	device, err := NewDevice(context.Background(), Identity{}, transport)
	if device != nil || !errors.Is(err, ErrUncertain) || closes != 1 {
		t.Fatalf("device=%v err=%v closes=%d", device, err, closes)
	}
}

func TestCapacityAndWriteIssueCallbackAbortBeforeNativeCommand(t *testing.T) {
	// Accounting rejection is actual pre-command evidence, unlike a queued operation or a failed ioctl.
	calls := 0
	transport := newSerializedTransport(func(Command) (Response, error) { calls++; return Response{Completed: true}, nil }, func() error { return nil })
	defer transport.Close()
	cause := errors.New("admission stopped")
	capacity := &Capacity{transport: transport}
	if sample, issued, err := capacity.Observe(context.Background(), func() error { return cause }); sample != nil || issued || !errors.Is(err, cause) || calls != 0 {
		t.Fatalf("sample=%v issued=%t err=%v calls=%d", sample, issued, err, calls)
	}
	records := Records{Transport: transport, Mode: RecordMode{MaximumBytes: 512}, Timeout: time.Second}
	result, err := records.WriteObserved(context.Background(), make([]byte, 512), func() error { return cause })
	if !result.AcceptedKnown || result.Bytes != 0 || !errors.Is(err, cause) || calls != 0 {
		t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
	}
	if _, err := records.Write(context.Background(), make([]byte, 512)); err != nil || calls != 1 {
		t.Fatalf("later WRITE=%v calls=%d", err, calls)
	}
}

func TestCapacityUnknownPhysicalOutcomePoisonsFollowingCommands(t *testing.T) {
	// A sample error cannot erase native uncertainty or authorize another command/cleanup release.
	calls := 0
	transport := newSerializedTransport(func(Command) (Response, error) { calls++; return Response{Completed: true, HostStatus: 8}, nil }, func() error { return nil })
	capacity := &Capacity{transport: transport}
	if _, issued, err := capacity.Observe(context.Background(), nil); !issued || !errors.Is(err, ErrUncertain) {
		t.Fatalf("issued=%t err=%v", issued, err)
	}
	if _, issued, err := capacity.Observe(context.Background(), nil); issued || !errors.Is(err, ErrUncertain) || calls != 1 {
		t.Fatalf("later issued=%t err=%v calls=%d", issued, err, calls)
	}
	if err := transport.Close(); !errors.Is(err, ErrUncertain) {
		t.Fatalf("cleanup dropped uncertainty: %v", err)
	}
}

func TestCapacityInspectionFailuresRemainFeatureLocal(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		// Unknown optional capacity cannot revoke the independently observed baseline operation facts.
		transport := newSerializedTransport(func(command Command) (Response, error) {
			if command.CDB[0] == 0x4d {
				if malformed {
					return Response{Completed: true, Residual: int32(len(command.Data) - 1)}, nil
				}
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 5, 0x20, 0, false, 0, 0)}, nil
			}
			return inspectionFixture(command)
		}, func() error { return nil })
		defer transport.Close()
		report, err := inspectTransport(context.Background(), Identity{}, transport)
		if err != nil || report.Reason != 0 {
			t.Fatalf("optional capacity invalidated baseline: %v %v", report, err)
		}
		f := feature(report, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING)
		if f.Reason == 0 || f.GetCapacity() != nil || f.Evidence != entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_UNSPECIFIED {
			t.Fatalf("failed fresh sample invented evidence: %v", f)
		}
	}
}

func TestCapacityInspectionReadinessLossOverridesEarlierReadyObservation(t *testing.T) {
	for _, noMedium := range []bool{false, true} {
		// A clean capacity rejection can reveal actual medium loss/not-ready after TUR initially succeeded.
		transport := newSerializedTransport(func(command Command) (Response, error) {
			if command.CDB[0] == 0x4d {
				asc := byte(4)
				if noMedium {
					asc = 0x3a
				}
				return Response{Completed: true, Status: 2, Sense: fixedSense(0x70, 2, asc, 0, false, 0, 0)}, nil
			}
			return inspectionFixture(command)
		}, func() error { return nil })
		defer transport.Close()
		report, err := inspectTransport(context.Background(), Identity{}, transport)
		want := entity.DeviceReason_DEVICE_REASON_NOT_READY
		if noMedium {
			want = entity.DeviceReason_DEVICE_REASON_NO_MEDIUM
		}
		if err != nil || report.Reason != want || report.GetTape().GetMediumPresent() == noMedium {
			t.Fatalf("readiness loss no-medium=%t report=%v err=%v", noMedium, report, err)
		}
		for _, kind := range []entity.DeviceFeatureKind{entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_READ, entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_SEQUENTIAL_WRITE} {
			f := feature(report, kind)
			if f.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNAVAILABLE || f.Reason != want {
				t.Fatalf("baseline remained available after readiness loss: %v", f)
			}
		}
	}
}

func TestCapacityCounterSupportDoesNotDisappearWithUnknownValue(t *testing.T) {
	for _, complete := range []bool{false, true} {
		// Observed standardized counters remain supported when their present value is a valid unknown sentinel.
		device := &Device{report: newReport(Identity{})}
		sample := &CapacitySample{Partition: 7, IssuedAt: time.Now(), CompletedAt: time.Now(), Quality: CapacityUnknown}
		support, quality := entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_PARTIAL, entity.TapeCapacityQuality_TAPE_CAPACITY_QUALITY_UNSPECIFIED
		if complete {
			sample.Quality = CapacityEstimate
			support, quality = entity.DeviceFeatureSupport_DEVICE_FEATURE_SUPPORT_SUPPORTED, entity.TapeCapacityQuality_TAPE_CAPACITY_QUALITY_ESTIMATE
		}
		device.reportCapacity(sample, true, nil)
		f := feature(device.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING)
		if f.Support != support || f.Availability != entity.DeviceFeatureAvailability_DEVICE_FEATURE_AVAILABILITY_UNKNOWN ||
			f.GetCapacity().RemainingBytes != nil || f.GetCapacity().Quality != quality || f.GetCapacity().GetPartition() != 7 {
			t.Fatalf("known counter support confused with unknown value: %v", f)
		}

		// A failed fresh query clears this observation; no stale field or evidence becomes a new sample.
		device.reportCapacity(nil, true, ErrMalformed)
		f = feature(device.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING)
		if f.GetCapacity() != nil || f.Evidence != entity.DeviceFeatureEvidence_DEVICE_FEATURE_EVIDENCE_UNSPECIFIED {
			t.Fatalf("failed query retained fresh evidence: %v", f)
		}
	}
}

func TestCapacityPreparationKeepsEstimateAllowancesAndQualificationUnknown(t *testing.T) {
	// A configured single partition permits one estimate without enabling continuous polling.
	d := fixtureDevice(t, newDeviceFixture())
	if _, err := d.Configure(context.Background(), &entity.TarMediaProfile{RecordMode: entity.TapeRecordMode_TAPE_RECORD_MODE_FIXED, RecordBytes: 512}, Read); err != nil {
		t.Fatal(err)
	}
	capacity, err := d.Capacity(context.Background())
	if err != nil || capacity == nil || capacity.Initial == nil {
		t.Fatalf("capacity=%v err=%v", capacity, err)
	}
	sample := capacity.Initial
	if sample.Partition != 0 || sample.Quality != CapacityEstimate || sample.RemainingBytes == nil || *sample.RemainingBytes != 100_000_000 ||
		sample.ErrorBytes != nil || sample.UnreflectedBytes != nil || sample.IssuedAt.IsZero() || sample.CompletedAt.Before(sample.IssuedAt) {
		t.Fatalf("estimate=%+v", sample)
	}
	f := feature(d.Report(), entity.DeviceFeatureKind_DEVICE_FEATURE_KIND_CAPACITY_TO_WARNING)
	if f.Validation != entity.DeviceFeatureValidation_DEVICE_FEATURE_VALIDATION_UNVALIDATED ||
		f.GetCapacity().ObservationErrorBytes != nil || f.GetCapacity().UnreflectedBytes != nil {
		t.Fatalf("sample self-qualified: %v", f)
	}
	if err := d.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}
