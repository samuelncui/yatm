package media

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
)

func TestTarCapacityWriteCallbackCannotAdvanceFullReturnPrefix(t *testing.T) {
	// Pause a WRITE after its issue callback but before the synthetic native submission.
	granted, releaseWrite := make(chan struct{}), make(chan struct{})
	device := &tarCapacityTestDevice{tarTestDevice: &tarTestDevice{}, afterIssue: func() {
		close(granted)
		<-releaseWrite
	}}
	options := tarTestOptions(t, false, 4096)
	options.Capacity = tarCapacityTestOptions(100000)
	options.Capacity.Interval = time.Hour
	options.Capacity.Observe = func(ctx context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
		// This query starts after the WRITE callback; none of that WRITE is yet known accepted.
		select {
		case <-granted:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
		if err := issue(); err != nil {
			return nil, false, err
		}
		return tarCapacityTestSample(10000, tape.CapacityLowerBound), true, nil
	}
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	ref, err := session.Target("member", 3584)
	require.NoError(t, err)
	writer, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "member", size: 3584}}})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { _, err := writer.Write(make([]byte, 3584)); done <- err }()
	<-granted

	// Publication must debit the entire reserved member, rather than omit the pre-syscall WRITE.
	budget := session.tarWriteSession.capacity
	require.Eventually(t, func() bool {
		budget.mu.Lock()
		defer budget.mu.Unlock()
		return budget.available == 10000-4160-1344 && budget.returnedCost == 0
	}, time.Second, time.Millisecond)
	close(releaseWrite)
	require.NoError(t, <-done)
	require.NoError(t, writer.Finish(nil))
	device.afterIssue = nil
	budget.mu.Lock()
	returned, available := budget.returnedCost, budget.available
	budget.mu.Unlock()
	require.Equal(t, uint64(4160), returned)
	require.Equal(t, uint64(10000-4160-1344), available, "later full return must not replenish an overlapping sample")

	// Normal completion still flushes the trailer and publishes exactly the completed member.
	result, err := session.Finalize(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tarTestCompleted(t, session, result), 1)
}

type tarCapacityResultDevice struct {
	*tarTestDevice
	result tape.RecordResult
	err    error
}

func (d *tarCapacityResultDevice) Write(context.Context, []byte) (tape.RecordResult, error) {
	return d.result, d.err
}

func (d *tarCapacityResultDevice) WriteObserved(ctx context.Context, data []byte, issue func() error) (tape.RecordResult, error) {
	if err := issue(); err != nil {
		return tape.RecordResult{AcceptedKnown: true}, err
	}
	return d.Write(ctx, data)
}

func TestTarCapacityOnlyKnownFullReturnedRecordsAdvancePrefix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result tape.RecordResult
		err    error
		full   bool
	}{
		{"full", tape.RecordResult{Bytes: 4096, AcceptedKnown: true}, nil, true},
		{"full with error", tape.RecordResult{Bytes: 4096, AcceptedKnown: true}, errors.New("write failure"), true},
		{"partial", tape.RecordResult{Bytes: 2048, AcceptedKnown: true}, nil, false},
		{"unknown", tape.RecordResult{Bytes: 4096}, nil, false},
		{"zero", tape.RecordResult{AcceptedKnown: true}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Exercise the live return-side path without starting an unrelated background worker.
			options := tarCapacityTestOptions(100000)
			options.Interval = time.Hour
			options.Observe = func(context.Context, func() error) (*tape.CapacitySample, bool, error) { return nil, false, nil }
			budget, err := newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
			require.NoError(t, err)
			require.NoError(t, budget.reserve(8192))
			device := &tarCapacityResultDevice{tarTestDevice: &tarTestDevice{}, result: tc.result, err: tc.err}
			session := &tarWriteSession{device: device, buffer: make([]byte, 4096), capacity: budget}
			err = session.writeRecord()
			if tc.full {
				require.Equal(t, uint64(4096), budget.returnedBytes)
				require.Equal(t, uint64(4160), budget.returnedCost)
				require.Equal(t, uint64(4096), session.accepted)
			} else {
				require.Zero(t, budget.returnedBytes)
				require.Zero(t, budget.returnedCost)
				require.Zero(t, session.accepted)
			}
			if tc.name == "full" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				require.Error(t, session.unusable)
			}
		})
	}
}

func TestTarCapacityOffDoesNotAccountEachReturnedWrite(t *testing.T) {
	// Initial-only capacity still charges member admission, without a per-record observer cost.
	calls := 0
	options := tarTestOptions(t, false, 4096)
	options.Capacity = &TarCapacityOptions{Initial: tarCapacityTestSample(100000, tape.CapacityEstimate),
		Cost: func(encoded, _ uint64, _ uint32) (uint64, error) {
			calls++
			return encoded, nil
		}}
	device := &tarTestDevice{}
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	ref, err := session.Target("member", 8192)
	require.NoError(t, err)
	writer, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "member", size: 8192}}})
	require.NoError(t, err)
	admissionCalls := calls

	// Multiple synthetic-device record returns add no Cost calls before or during completion.
	_, err = writer.Write(make([]byte, 8192))
	require.NoError(t, err)
	require.NoError(t, writer.Finish(nil))
	require.Equal(t, 2, device.writes)
	require.Equal(t, admissionCalls, calls)
	result, err := session.Finalize(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tarTestCompleted(t, session, result), 1)
	require.Equal(t, admissionCalls, calls)
}

func TestTarCapacityOverlapSaturatesWithoutReclaimingInFlightWrites(t *testing.T) {
	for _, tc := range []struct {
		name                           string
		remaining, captured, allowance uint64
		want                           uint64
	}{
		{"known return before query", 20000, 4160, 0, 20000 - 4160 - 1344},
		{"return during query", 20000, 0, 0, 20000 - 8320 - 1344},
		{"small sample", 100, 0, 0, 0},
		{"maximum allowance", math.MaxUint64, 0, math.MaxUint64, 0},
		{"maximum sample", math.MaxUint64, 0, 0, math.MaxUint64 - 8320 - 1344},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The returned prefix may grow after capture; publication still subtracts the captured K.
			options := tarCapacityTestOptions(100000)
			options.Initial = nil
			budget, err := newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
			require.NoError(t, err)
			require.NoError(t, budget.reserve(8192))
			require.NoError(t, budget.returned(4096))
			sample := tarCapacityTestSample(tc.remaining, tape.CapacityLowerBound)
			sample.ErrorBytes, sample.UnreflectedBytes = &tc.allowance, &tc.allowance
			budget.publish(sample, tarCapacityIssue{returned: tc.captured})
			require.Equal(t, tc.want, budget.available)
		})
	}
}

func TestTarCapacityFullReturnCounterFailurePoisonsBudget(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprintf("overflow=%t", overflow), func(t *testing.T) {
			// A policy/counter failure cannot create a later trusted prefix or permit more admissions.
			budget, err := newTarCapacityBudget(tarCapacityTestOptions(100000), tape.RecordMode{MaximumBytes: 4096})
			require.NoError(t, err)
			if overflow {
				budget.returnedBytes = math.MaxUint64
			} else {
				budget.returnedCost = math.MaxUint64
			}
			require.Error(t, budget.returned(4096))
			require.Error(t, budget.reserve(512))
			require.Error(t, budget.stop())
		})
	}
}

func TestTarCapacityFatalQueryRetainsPregrantedWriteWithoutPublication(t *testing.T) {
	for _, failure := range []error{errors.New("query unavailable"), &tape.UncertainError{Cause: tape.ErrMalformed},
		errors.Join(context.Canceled, tape.ErrUncertain), errors.Join(tape.ErrUnsupported, errors.New("cleanup failed"))} {
		t.Run(failure.Error(), func(t *testing.T) {
			// Hold the sole WRITE after its grant; the overlapping query can now poison the attempt.
			granted, releaseWrite := make(chan struct{}), make(chan struct{})
			device := &tarCapacityTestDevice{tarTestDevice: &tarTestDevice{}, afterIssue: func() {
				close(granted)
				<-releaseWrite
			}}
			options := tarTestOptions(t, false, 4096)
			options.Capacity = tarCapacityTestOptions(100000)
			options.Capacity.Interval = time.Hour
			options.Capacity.Observe = func(ctx context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
				select {
				case <-granted:
				case <-ctx.Done():
					return nil, false, ctx.Err()
				}
				if err := issue(); err != nil {
					return nil, false, err
				}
				return nil, true, failure
			}
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			ref, err := session.Target("member", 3584)
			require.NoError(t, err)
			writer, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "member", size: 3584}}})
			require.NoError(t, err)
			done := make(chan error, 1)
			go func() { _, err := writer.Write(make([]byte, 3584)); done <- err }()
			<-granted
			budget := session.tarWriteSession.capacity
			budget.worker.Wait()

			// The granted WRITE settles fully; its accounting is retained despite the fatal observation.
			close(releaseWrite)
			cause := <-done
			require.ErrorIs(t, cause, failure)
			require.ErrorIs(t, writer.Finish(cause), failure)
			require.Equal(t, uint64(4096), budget.returnedBytes)
			require.Equal(t, 1, device.writes)
			ref, err = session.Target("later", 1)
			require.NoError(t, err)
			_, err = ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "later", size: 1}}})
			require.ErrorIs(t, err, failure)

			// No content completion or publication follows; Finalize still owns the joined shutdown.
			result, err := session.Finalize(context.Background(), cause)
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrFinalizeUnusable)
			require.ErrorIs(t, err, failure)
			require.Zero(t, device.marks)
			require.Equal(t, 1, device.writes)
		})
	}
}

func TestTarCapacityShutdownKeepsLocalMissesOptional(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		clean   bool
	}{
		{"canceled", context.Canceled, true},
		{"wrapped cancel", fmt.Errorf("observation stopped, %w", context.Canceled), true},
		{"joined cancel", errors.Join(context.Canceled, context.DeadlineExceeded), true},
		{"joined payload", errors.Join(context.Canceled, tape.ErrMalformed), true},
		{"joined optional", errors.Join(context.Canceled, tape.ErrUnsupported), true},
		{"joined native malformed", errors.Join(context.Canceled, &tape.UncertainError{Cause: tape.ErrMalformed}), false},
		{"joined cleanup failure", errors.Join(context.Canceled, errors.New("cleanup failed")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Owned shutdown permits local misses, while native uncertainty and unrelated causes stay fatal.
			issued := make(chan struct{})
			options := tarCapacityTestOptions(10000)
			options.Interval = time.Hour
			options.Observe = func(ctx context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
				if err := issue(); err != nil {
					return nil, false, err
				}
				close(issued)
				<-ctx.Done()
				return nil, true, tc.failure
			}
			budget, err := newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
			require.NoError(t, err)
			budget.start()
			<-issued
			err = budget.stop()
			if tc.clean {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tc.failure)
			}
		})
	}
}
