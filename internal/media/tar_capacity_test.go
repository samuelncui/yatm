package media

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
)

func tarCapacityTestCost(encoded, records uint64, marks uint32) (uint64, error) {
	if records > (math.MaxUint64-uint64(marks)*256)/64 || encoded > math.MaxUint64-records*64-uint64(marks)*256 {
		return 0, errors.New("cost overflow")
	}
	return encoded + records*64 + uint64(marks)*256, nil
}

func tarCapacityTestSample(remaining uint64, quality tape.CapacityQuality) *tape.CapacitySample {
	now := time.Now().Add(-time.Millisecond)
	zero := uint64(0)
	return &tape.CapacitySample{IssuedAt: now, CompletedAt: now, RemainingBytes: &remaining,
		Quality: quality, ErrorBytes: &zero, UnreflectedBytes: &zero}
}

func tarCapacityTestOptions(remaining uint64) *TarCapacityOptions {
	return &TarCapacityOptions{Initial: tarCapacityTestSample(remaining, tape.CapacityLowerBound),
		MaxAge: time.Hour, Cost: tarCapacityTestCost, CostBounded: true}
}

type tarCapacityTestDevice struct {
	*tarTestDevice
	afterIssue func()
}

func (d *tarCapacityTestDevice) WriteObserved(ctx context.Context, data []byte, issued func() error) (tape.RecordResult, error) {
	if err := issued(); err != nil {
		return tape.RecordResult{AcceptedKnown: true}, err
	}
	if d.afterIssue != nil {
		d.afterIssue()
	}
	return d.Write(ctx, data)
}

func TestTarCapacityUsesObservedMetadataAndCapturedPAX(t *testing.T) {
	for _, pax := range []bool{false, true} {
		t.Run(fmt.Sprintf("pax=%t", pax), func(t *testing.T) {
			// A one-record member fits; observed payload/PAX larger than the Target hint does not.
			options := tarTestOptions(t, false, 4096)
			options.Capacity = tarCapacityTestOptions(1344 + 1088)
			if pax {
				options.Capacity = tarCapacityTestOptions(1344 + 2112)
			}
			device := &tarTestDevice{}
			session, err := newTarTestWriteSession(device, options)
			require.NoError(t, err)
			ref, err := session.Target("member", math.MaxInt64)
			require.NoError(t, err)
			metadata := acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "member", size: 1}}}
			writer, err := ref.Content.Open(context.Background(), metadata)
			require.NoError(t, err)
			_, err = writer.Write([]byte{1})
			require.NoError(t, err)
			require.NoError(t, writer.Finish(nil))
			ref, err = session.Target("later", 0)
			require.NoError(t, err)
			metadata.Info = tarWritePositionInfo{tarTestInfo{name: "later", size: 1}}
			if pax {
				metadata.Xattrs = map[string][]byte{"user.large": make([]byte, 1500)}
			} else {
				metadata.Info = tarWritePositionInfo{tarTestInfo{name: "later", size: 8192}}
			}
			_, cause := ref.Content.Open(context.Background(), metadata)
			require.ErrorIs(t, cause, ErrTargetNoSpace)
			require.ErrorContains(t, cause, "want=")
			require.ErrorContains(t, cause, "quality=lower bound")
			require.Zero(t, device.writes)

			// Cached refusal seals the buffered member with a real normal trailer and one mark.
			result, err := session.Finalize(context.Background(), cause)
			require.NoError(t, err)
			require.ErrorIs(t, result.Stop, ErrCapacityBoundary)
			require.Len(t, tarTestCompleted(t, session, result), 1)
			require.Equal(t, 1, device.writes)
			require.Equal(t, 1, device.marks)
			reader := tar.NewReader(bytes.NewReader(device.records[0].data))
			header, err := reader.Next()
			require.NoError(t, err)
			require.Equal(t, tarPrefix(1)+"/member", header.Name)
			_, err = reader.Next()
			require.ErrorIs(t, err, io.EOF)
		})
	}
}

func TestTarCapacityPackedMembersChargeSharedRecordsOnce(t *testing.T) {
	// Eight zero-length headers exactly fill one record; record overhead is charged once.
	device := &tarTestDevice{}
	options := tarTestOptions(t, false, 4096)
	options.Capacity = tarCapacityTestOptions(1344 + 4096 + 64)
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	for i := range 8 {
		writeTarPositionMember(t, session, fmt.Sprintf("member%d", i), 0)
	}
	ref, err := session.Target("refused", 0)
	require.NoError(t, err)
	_, cause := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "refused"}}})
	require.ErrorIs(t, cause, ErrTargetNoSpace)
	result, err := session.Finalize(context.Background(), cause)
	require.NoError(t, err)
	require.Len(t, tarTestCompleted(t, session, result), 8)
	require.Equal(t, 2, device.writes)
	require.Equal(t, 1, device.marks)
}

func TestTarCapacityAbsoluteSamplesUseFullReturnsAndNewReservations(t *testing.T) {
	// Initial unknown permits baseline admission; only known full returns reduce the sampled debit.
	options := tarCapacityTestOptions(100000)
	options.Initial = nil
	budget, err := newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
	require.NoError(t, err)
	require.NoError(t, budget.reserve(8192))
	require.NoError(t, budget.returned(4096))
	issue := tarCapacityIssue{returned: 4160}
	remaining, allowance := uint64(20000), uint64(300)
	sample := tarCapacityTestSample(remaining, tape.CapacityLowerBound)
	sample.UnreflectedBytes = &allowance
	// The response already arrived; a later admission still belongs to A until atomic publication.
	require.NoError(t, budget.reserve(1024))
	budget.publish(sample, issue)
	require.Equal(t, uint64(20000-4160-1088-1344-300), budget.available)

	// Repeated absolute observations do not debit prior, fully returned lifetime bytes again.
	require.NoError(t, budget.returned(4096))
	issue = tarCapacityIssue{returned: 8320}
	sample = tarCapacityTestSample(remaining, tape.CapacityLowerBound)
	sample.IssuedAt = sample.IssuedAt.Add(time.Microsecond)
	sample.CompletedAt = sample.IssuedAt
	budget.publish(sample, issue)
	require.Equal(t, uint64(20000-1088-1344), budget.available)
	previous := budget.available
	budget.publish(sample, issue)
	require.Equal(t, previous, budget.available)
}

func TestTarCapacityEstimateCannotReplenishBoundAndUnknownFreshnessIsEstimated(t *testing.T) {
	// Approximate refresh cannot increase a still-applicable lower bound or extend its expiry.
	options := tarCapacityTestOptions(10000)
	budget, err := newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
	require.NoError(t, err)
	require.NoError(t, budget.reserve(1024))
	before, expiry := budget.available, budget.validUntil
	estimate := tarCapacityTestSample(100000, tape.CapacityEstimate)
	estimate.IssuedAt = estimate.IssuedAt.Add(time.Microsecond)
	estimate.CompletedAt = estimate.IssuedAt
	budget.publish(estimate, tarCapacityIssue{})
	require.Equal(t, before, budget.available)
	require.Equal(t, tape.CapacityLowerBound, budget.quality)
	require.Equal(t, expiry, budget.validUntil)

	// Missing freshness/cost/error terms never manufacture a bound; the estimate remains best effort.
	options.MaxAge = 0
	options.Initial.IssuedAt = time.Now().Add(-24 * time.Hour)
	options.Initial.CompletedAt = options.Initial.IssuedAt
	options.Initial.ErrorBytes, options.Initial.UnreflectedBytes = nil, nil
	budget, err = newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
	require.NoError(t, err)
	require.True(t, budget.known)
	require.Equal(t, tape.CapacityEstimate, budget.quality)
	require.True(t, budget.validUntil.IsZero())
	require.ErrorContains(t, budget.reserve(100000), "quality=estimate")
}

func TestTarCapacityStaleUnknownAndInvalidSamplesCannotCreateHeadroom(t *testing.T) {
	for name, change := range map[string]func(*tape.CapacitySample){
		"stale":       func(s *tape.CapacitySample) { s.IssuedAt = time.Now().Add(-2 * time.Hour); s.CompletedAt = s.IssuedAt },
		"unknown":     func(s *tape.CapacitySample) { s.RemainingBytes = nil },
		"bad quality": func(s *tape.CapacitySample) { s.Quality = 255 },
		"future":      func(s *tape.CapacitySample) { s.CompletedAt = time.Now().Add(time.Hour) },
		"partition":   func(s *tape.CapacitySample) { s.Partition = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			options := tarCapacityTestOptions(10000)
			options.Initial = nil
			budget, err := newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
			require.NoError(t, err)
			sample := tarCapacityTestSample(100000, tape.CapacityLowerBound)
			change(sample)
			budget.publish(sample, tarCapacityIssue{})
			require.False(t, budget.known)
			require.NoError(t, budget.reserve(1000000))
		})
	}
}

func TestTarCapacityPendingObserverDoesNotBlockAdmissionOrWrite(t *testing.T) {
	// An observer paused before actual command issue owns no data lane or admission lock.
	started := make(chan struct{})
	options := tarTestOptions(t, false, 4096)
	options.Capacity = tarCapacityTestOptions(100000)
	options.Capacity.Interval = time.Hour
	options.Capacity.Observe = func(ctx context.Context, _ func() error) (*tape.CapacitySample, bool, error) {
		close(started)
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
	device := &tarCapacityTestDevice{tarTestDevice: &tarTestDevice{}}
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	<-started
	writeTarPositionMember(t, session, "member", 3584)
	require.Equal(t, 1, device.writes)
	result, err := session.Finalize(context.Background(), nil)
	require.NoError(t, err)
	require.Nil(t, result.Stop)
	require.Len(t, tarTestCompleted(t, session, result), 1)
}

func TestTarCapacityObserverPublicationIncludesOverlappingAdmissions(t *testing.T) {
	// Hold a query after issue; the writer may admit another buffered member before publication.
	allowIssue, issued, respond := make(chan struct{}), make(chan struct{}), make(chan struct{})
	options := tarTestOptions(t, false, 4096)
	options.Capacity = tarCapacityTestOptions(100000)
	options.Capacity.Interval = time.Hour
	options.Capacity.Observe = func(ctx context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
		select {
		case <-allowIssue:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
		if err := issue(); err != nil {
			return nil, false, err
		}
		close(issued)
		select {
		case <-respond:
		case <-ctx.Done():
			return nil, true, ctx.Err()
		}
		return tarCapacityTestSample(10000, tape.CapacityLowerBound), true, nil
	}
	device := &tarCapacityTestDevice{tarTestDevice: &tarTestDevice{}}
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	writeTarPositionMember(t, session, "first", 1)
	close(allowIssue)
	<-issued
	writeTarPositionMember(t, session, "second", 1)
	close(respond)
	budget := session.tarWriteSession.capacity
	require.Eventually(t, func() bool {
		budget.mu.Lock()
		defer budget.mu.Unlock()
		return budget.available == 10000-1088-1024-1344
	}, time.Second, time.Millisecond)
	result, err := session.Finalize(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tarTestCompleted(t, session, result), 2)
}

func TestTarCapacityShutdownUncertaintyWithholdsPublication(t *testing.T) {
	// A command issued before shutdown must settle, and uncertainty cannot hide behind cancellation.
	issued := make(chan struct{})
	options := tarTestOptions(t, false, 4096)
	options.Capacity = tarCapacityTestOptions(100000)
	options.Capacity.Interval = time.Hour
	options.Capacity.Observe = func(ctx context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
		if err := issue(); err != nil {
			return nil, false, err
		}
		close(issued)
		<-ctx.Done()
		return nil, true, errors.Join(ctx.Err(), tape.ErrUncertain)
	}
	device := &tarCapacityTestDevice{tarTestDevice: &tarTestDevice{}}
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	<-issued
	writeTarPositionMember(t, session, "member", 1)
	result, err := session.Finalize(context.Background(), nil)
	require.Nil(t, result)
	require.ErrorIs(t, err, tape.ErrUncertain)
	require.ErrorIs(t, err, ErrFinalizeUnusable)
	require.Zero(t, device.writes)
	require.Zero(t, device.marks)
}

func TestTarCapacityPositiveBudgetCannotOverrideWriteWarning(t *testing.T) {
	// Real WRITE warning suppresses all later data, even though cached capacity remains positive.
	device := &tarTestDevice{warningAt: 1}
	options := tarTestOptions(t, false, 4096)
	options.Capacity = tarCapacityTestOptions(100000)
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	ref, err := session.Target("member", 3584)
	require.NoError(t, err)
	writer, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "member", size: 3584}}})
	require.NoError(t, err)
	_, cause := writer.Write(make([]byte, 3584))
	require.ErrorIs(t, cause, tape.ErrEarlyWarning)
	_ = writer.Finish(cause)
	require.Equal(t, uint64(4096), session.tarWriteSession.capacity.reservedBytes)
	require.Equal(t, uint64(4160), session.tarWriteSession.capacity.reservedCost)
	ref, err = session.Target("later", 1)
	require.NoError(t, err)
	_, err = ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "later", size: 1}}})
	require.ErrorIs(t, err, tape.ErrEarlyWarning)
	result, err := session.Finalize(context.Background(), cause)
	require.NoError(t, err)
	require.ErrorIs(t, result.Stop, tape.ErrEarlyWarning)
	require.Equal(t, 1, device.writes)
	require.Equal(t, 1, device.marks)
}

func TestTarCapacityIssuedObserverDoesNotBlockWrite(t *testing.T) {
	// A synthetic independent query stays issued while a complete WRITE returns on its own lane.
	issued, respond := make(chan struct{}), make(chan struct{})
	device := &tarCapacityTestDevice{tarTestDevice: &tarTestDevice{}}
	options := tarTestOptions(t, false, 4096)
	options.Capacity = tarCapacityTestOptions(100000)
	options.Capacity.Interval = time.Hour
	options.Capacity.Observe = func(ctx context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
		if err := issue(); err != nil {
			return nil, false, err
		}
		close(issued)
		select {
		case <-respond:
		case <-ctx.Done():
			return nil, true, ctx.Err()
		}
		return tarCapacityTestSample(10000, tape.CapacityLowerBound), true, nil
	}
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	<-issued
	ref, err := session.Target("member", 3584)
	require.NoError(t, err)
	writer, err := ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "member", size: 3584}}})
	require.NoError(t, err)

	// The query's start prefix excludes this return even if the device already reflected its bytes.
	done := make(chan error, 1)
	go func() { _, err := writer.Write(make([]byte, 3584)); done <- err }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("WRITE waited for held capacity query")
	}
	require.NoError(t, writer.Finish(nil))
	budget := session.tarWriteSession.capacity
	budget.mu.Lock()
	returned := budget.returnedCost
	budget.mu.Unlock()
	require.Equal(t, uint64(4160), returned)
	close(respond)
	require.Eventually(t, func() bool {
		budget.mu.Lock()
		defer budget.mu.Unlock()
		return budget.available == 10000-4160-1344
	}, time.Second, time.Millisecond)

	// Normal shutdown joins the held observer before flushing the remaining standard trailer.
	result, err := session.Finalize(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tarTestCompleted(t, session, result), 1)
}

func TestTarCapacityRefreshFailureRetainsDebitedBudget(t *testing.T) {
	for _, failure := range []error{tape.ErrUnsupported, tape.ErrMalformed, errors.New("observation unavailable")} {
		t.Run(failure.Error(), func(t *testing.T) {
			// Optional counter failures never replenish cached space; unknown failures remain fatal.
			returned := make(chan struct{})
			options := tarCapacityTestOptions(10000)
			options.Interval = time.Hour
			options.Observe = func(_ context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
				if err := issue(); err != nil {
					return nil, false, err
				}
				close(returned)
				return nil, true, failure
			}
			budget, err := newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
			require.NoError(t, err)
			require.NoError(t, budget.reserve(1024))
			before := budget.available
			budget.start()
			<-returned
			if tape.CleanUnsupported(failure) || failure == tape.ErrMalformed {
				require.NoError(t, budget.stop())
			} else {
				require.ErrorIs(t, budget.stop(), failure)
			}
			require.Equal(t, before, budget.available)
			require.Equal(t, tape.CapacityLowerBound, budget.quality)
		})
	}
}

func TestTarCapacityNoMediumObservationFailsAttempt(t *testing.T) {
	// A real device-wide failure is not an optional missing estimate.
	failure := &tape.CommandError{Response: tape.Response{Completed: true, Status: 2}, Sense: tape.Sense{Key: 2, ASC: 0x3a}}
	options := tarCapacityTestOptions(10000)
	options.Interval = time.Hour
	options.Observe = func(_ context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
		if err := issue(); err != nil {
			return nil, false, err
		}
		return nil, true, failure
	}
	budget, err := newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
	require.NoError(t, err)
	budget.start()
	budget.worker.Wait()
	require.ErrorIs(t, budget.reserve(1024), failure)
	require.ErrorIs(t, budget.stop(), failure)
}

func TestTarCapacityExpiryAndFailedReservations(t *testing.T) {
	// Expired bounded evidence becomes unknown; previously admitted/failed work is never reclaimed.
	options := tarCapacityTestOptions(10000)
	budget, err := newTarCapacityBudget(options, tape.RecordMode{MaximumBytes: 4096})
	require.NoError(t, err)
	require.NoError(t, budget.reserve(1024))
	before := budget.reservedCost
	require.ErrorIs(t, budget.reserve(10000), ErrTargetNoSpace)
	require.Equal(t, before, budget.reservedCost)
	budget.validUntil = time.Now().Add(-time.Second)
	require.NoError(t, budget.reserve(10000))
	require.False(t, budget.known)
	require.Greater(t, budget.reservedCost, before)
}

func TestTarCapacityLiveRequiresActualSubmissionHook(t *testing.T) {
	options := tarTestOptions(t, false, 4096)
	options.Capacity = tarCapacityTestOptions(10000)
	options.Capacity.Interval = time.Hour
	options.Capacity.Observe = func(context.Context, func() error) (*tape.CapacitySample, bool, error) { return nil, false, nil }
	session, err := newTarTestWriteSession(&tarTestDevice{}, options)
	require.Nil(t, session)
	require.ErrorContains(t, err, "actual WRITE issue")
}

func TestTarCapacityTerminalObservationStopsBeforePositionQuery(t *testing.T) {
	// Device-wide capacity failure closes the writer before optional navigation or content access.
	failure := &tape.CommandError{Response: tape.Response{Completed: true, Status: 2}, Sense: tape.Sense{Key: 2, ASC: 0x3a}}
	options := tarTestOptions(t, false, 4096)
	options.Capacity = tarCapacityTestOptions(100000)
	options.Capacity.Interval = time.Hour
	options.Capacity.Observe = func(_ context.Context, issue func() error) (*tape.CapacitySample, bool, error) {
		if err := issue(); err != nil {
			return nil, false, err
		}
		return nil, true, failure
	}
	navigation := newTarWritePositionDevice()
	options.Navigator = navigation
	device := &tarCapacityTestDevice{tarTestDevice: &tarTestDevice{}}
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	session.tarWriteSession.capacity.worker.Wait()
	ref, err := session.Target("member", 1)
	require.NoError(t, err)
	_, err = ref.Content.Open(context.Background(), acp.ContentMetadata{Info: tarWritePositionInfo{tarTestInfo{name: "member", size: 1}}})
	require.ErrorIs(t, err, failure)
	require.Zero(t, navigation.queries)
	result, err := session.Finalize(context.Background(), nil)
	require.Nil(t, result)
	require.ErrorIs(t, err, failure)
	require.Zero(t, device.writes)
	require.Zero(t, device.marks)
}

func TestTarCapacityBusyObserverDoesNotPublishOrScaleWithMembers(t *testing.T) {
	// A busy unissued observation contributes no evidence, regardless of how many members open.
	returned := make(chan struct{})
	options := tarTestOptions(t, false, 4096)
	options.Capacity = tarCapacityTestOptions(100000)
	options.Capacity.Interval = time.Hour
	options.Capacity.Observe = func(context.Context, func() error) (*tape.CapacitySample, bool, error) {
		close(returned)
		return tarCapacityTestSample(0, tape.CapacityLowerBound), false, nil
	}
	device := &tarCapacityTestDevice{tarTestDevice: &tarTestDevice{}}
	session, err := newTarTestWriteSession(device, options)
	require.NoError(t, err)
	<-returned
	for i := range 100 {
		writeTarPositionMember(t, session, fmt.Sprintf("member%d", i), 0)
	}
	result, err := session.Finalize(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, tarTestCompleted(t, session, result), 100)
	require.Nil(t, result.Stop)
}
