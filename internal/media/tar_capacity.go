package media

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"syscall"
	"time"

	"github.com/samuelncui/yatm/internal/media/tape"
)

// TarCapacityOptions contains attempt-local qualified cost and observation facts.
// Cost is a monotone cumulative native-byte cost, including record and filemark overhead.
// When CostBounded is true, its increments must bound each extension under the qualified packing policy.
type TarCapacityOptions struct {
	Initial *tape.CapacitySample
	// Observe calls its callback synchronously when granting a query lifetime, before native submission.
	Observe     func(context.Context, func() error) (*tape.CapacitySample, bool, error)
	Interval    time.Duration
	MaxAge      time.Duration
	Cost        func(encodedBytes, records uint64, filemarks uint32) (uint64, error)
	CostBounded bool
}

type tarCapacityWriter interface {
	WriteObserved(context.Context, []byte, func() error) (tape.RecordResult, error)
}

type tarCapacityIssue struct {
	returned uint64
}

type tarCapacityRefusal struct {
	wanted, available uint64
	quality           tape.CapacityQuality
}

func (e *tarCapacityRefusal) Error() string {
	quality := "estimate"
	if e.quality == tape.CapacityLowerBound {
		quality = "lower bound"
	}
	return fmt.Sprintf("tar capacity admission stopped, want=%d have=%d native_bytes quality=%s", e.wanted, e.available, quality)
}

func (*tarCapacityRefusal) Unwrap() error { return ErrTargetNoSpace }

// tarCapacityBudget owns only cached admission arithmetic and one managed optional observer.
// Its mutex never spans device I/O; only known full WRITE returns advance the sampled prefix.
type tarCapacityBudget struct {
	mu                           sync.Mutex
	options                      TarCapacityOptions
	recordBytes, closure         uint64
	reservedBytes, returnedBytes uint64
	reservedCost, returnedCost   uint64
	available                    uint64
	quality                      tape.CapacityQuality
	validUntil, lastIssue        time.Time
	known, closed                bool
	failure                      error
	cancel                       context.CancelFunc
	worker                       sync.WaitGroup
}

func newTarCapacityBudget(options *TarCapacityOptions, mode tape.RecordMode) (*tarCapacityBudget, error) {
	if options == nil {
		return nil, nil
	}
	if options.Cost == nil || options.MaxAge < 0 || options.Interval < 0 || options.Observe != nil && options.Interval == 0 {
		return nil, errors.New("tar capacity requires a cost policy and valid observation age/cadence")
	}
	// Reserve both end blocks, fixed-mode final padding and the terminating filemark.
	// Charging up to two additional records is conservative even when the trailer shares a member record.
	recordBytes := uint64(mode.MaximumBytes)
	closureBytes := uint64(1024)
	if mode.FixedBytes != 0 {
		closureBytes += recordBytes - 512
	}
	closure, err := options.Cost(closureBytes, (closureBytes+recordBytes-1)/recordBytes, 1)
	if err != nil {
		return nil, fmt.Errorf("calculate tar completion capacity reserve failed, %w", err)
	}
	b := &tarCapacityBudget{options: *options, recordBytes: recordBytes, closure: closure}
	if options.Initial != nil {
		b.publish(options.Initial, tarCapacityIssue{})
	}
	return b, nil
}

func (b *tarCapacityBudget) start() {
	if b == nil || b.options.Observe == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	b.cancel = cancel
	b.worker.Add(1)
	go func() {
		defer b.worker.Done()
		ticker := time.NewTicker(b.options.Interval)
		defer ticker.Stop()
		for {
			// Capture only the prefix already known full before this query; overlap remains debited.
			var snapshot tarCapacityIssue
			captured := false
			sample, issued, err := b.options.Observe(ctx, func() error {
				b.mu.Lock()
				defer b.mu.Unlock()
				if b.closed {
					return context.Canceled
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				snapshot = tarCapacityIssue{returned: b.returnedCost}
				captured = true
				return nil
			})
			err = errors.Join(err, ctx.Err())

			// Optional capacity sample misses retain cached debits/EW and retry on the ordinary cadence.
			// Optional absence and clean shutdown cannot hide an unrelated or joined fatal cause.
			if err != nil {
				stopping := ctx.Err() != nil
				if !tarCapacityMiss(err, stopping) {
					b.mu.Lock()
					b.failure = errors.Join(b.failure, err)
					b.mu.Unlock()
					return
				}
				if stopping || tape.CleanUnsupported(err) {
					return
				}
			} else if issued && captured && sample != nil {
				b.publish(sample, snapshot)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func tarCapacityMiss(err error, stopping bool) bool {
	// Counter payload/deadline/busy and clean attachment resource failures retain the cached budget.
	if err == context.DeadlineExceeded || err == syscall.EBUSY || err == syscall.EMFILE || err == syscall.ENFILE ||
		err == syscall.ENOMEM || err == tape.ErrMalformed || stopping && err == context.Canceled {
		return true
	}
	if tape.CleanUnsupported(err) {
		return true
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return tarCapacityMiss(wrapped.Unwrap(), stopping)
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !tarCapacityMiss(cause, stopping) {
				return false
			}
		}
		return true
	}
	return false
}

func (b *tarCapacityBudget) cost(bytes uint64) (uint64, error) {
	records := bytes / b.recordBytes
	if bytes%b.recordBytes != 0 {
		records++
	}
	return b.options.Cost(bytes, records, 0)
}

func (b *tarCapacityBudget) err() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failure
}

func (b *tarCapacityBudget) reserve(bytes uint64) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failure != nil {
		return b.failure
	}
	if b.reservedBytes > math.MaxUint64-bytes {
		return errors.New("tar capacity reservation counter overflow")
	}
	cost, err := b.cost(b.reservedBytes + bytes)
	if err != nil || cost < b.reservedCost {
		return errors.Join(err, errors.New("tar capacity cost policy is not monotone"))
	}
	debit := cost - b.reservedCost
	if b.known && !b.validUntil.IsZero() && !time.Now().Before(b.validUntil) {
		b.known = false
	}
	if b.known && debit > b.available {
		return &tarCapacityRefusal{wanted: debit, available: b.available, quality: b.quality}
	}
	if b.known {
		b.available -= debit
	}
	b.reservedBytes += bytes
	b.reservedCost = cost
	return nil
}

func (b *tarCapacityBudget) returned(bytes uint64) error {
	// Retain full acceptance even when an overlapping query failed after the WRITE was granted.
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.returnedBytes > math.MaxUint64-bytes {
		b.failure = errors.Join(b.failure, errors.New("tar capacity full-return counter overflow"))
		return b.failure
	}
	cost, err := b.cost(b.returnedBytes + bytes)
	if err != nil || cost < b.returnedCost {
		b.failure = errors.Join(b.failure, err, errors.New("tar capacity full-return cost is not monotone"))
		return b.failure
	}
	b.returnedBytes += bytes
	b.returnedCost = cost
	return b.failure
}

func (b *tarCapacityBudget) publish(sample *tape.CapacitySample, issue tarCapacityIssue) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Absolute samples replace a budget; neither lifetime admissions nor old samples replenish it.
	now := time.Now()
	if sample.Partition != 0 {
		b.known = false
		return false
	}
	if sample.IssuedAt.IsZero() || sample.CompletedAt.Before(sample.IssuedAt) || sample.CompletedAt.After(now) ||
		!sample.IssuedAt.After(b.lastIssue) || b.options.MaxAge > 0 && !now.Before(sample.IssuedAt.Add(b.options.MaxAge)) || sample.IssuedAt.After(now) {
		return false
	}
	if sample.RemainingBytes == nil || sample.Quality != tape.CapacityEstimate && sample.Quality != tape.CapacityLowerBound {
		return false
	}
	quality := tape.CapacityEstimate
	if sample.Quality == tape.CapacityLowerBound && b.options.MaxAge > 0 && b.options.CostBounded && sample.ErrorBytes != nil && sample.UnreflectedBytes != nil {
		quality = tape.CapacityLowerBound
	}
	if issue.returned > b.returnedCost || issue.returned > b.reservedCost {
		return false
	}

	// Subtract separately to avoid overflow; WRITEs returned during the query are debited again.
	deductions := []uint64{b.reservedCost - issue.returned, b.closure}
	if sample.ErrorBytes != nil {
		deductions = append(deductions, *sample.ErrorBytes)
	}
	if sample.UnreflectedBytes != nil {
		deductions = append(deductions, *sample.UnreflectedBytes)
	}
	available := *sample.RemainingBytes
	for _, debit := range deductions {
		if debit >= available {
			available = 0
			break
		}
		available -= debit
	}
	if b.known && b.quality == tape.CapacityLowerBound && now.Before(b.validUntil) && quality != tape.CapacityLowerBound {
		// An estimate can tighten an applicable bound, but cannot increase it or renew its age.
		b.available = min(b.available, available)
		b.lastIssue = sample.IssuedAt
		return true
	}
	b.available, b.quality, b.known = available, quality, true
	b.validUntil, b.lastIssue = time.Time{}, sample.IssuedAt
	if b.options.MaxAge > 0 {
		b.validUntil = sample.IssuedAt.Add(b.options.MaxAge)
	}
	return true
}

func (b *tarCapacityBudget) stop() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	if b.cancel != nil {
		b.cancel()
	}
	b.worker.Wait()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failure
}
