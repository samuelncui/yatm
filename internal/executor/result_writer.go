package executor

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// resultWriterConcurrency is the writer bound of a results feed: at most this many batches are
// persisted at the same time, so one slow database cannot turn the feed into unbounded concurrent
// writes.
const resultWriterConcurrency = 8

// errResultWriterClosed reports an enqueue after Close, which is a caller that kept feeding results
// past the end of its run.
var errResultWriterClosed = errors.New("result writer is closed, it accepts no more results")

// ResultWriter persists the results a Job runner accepts, outside the caller's results callback.
// ACP owns the queue and the batch: it hands the callback one batch of at most the configured write
// batch size at a time, the callback only starts a write of that batch, and a semaphore keeps at
// most resultWriterConcurrency writes in flight, which is the one place where the callback waits.
//
// The first write error is recorded once and returned by Enqueue, which is how the run stops; the
// writes already accepted still run to completion, so a failed run never loses the results it
// reported. Close waits for every write and reports that error as the run's terminal error.
type ResultWriter[T any] struct {
	ctx   context.Context
	write func(context.Context, []T) error

	// sem is the concurrency semaphore: Enqueue takes a slot before it starts a writer.
	sem     chan struct{}
	writers sync.WaitGroup

	// sendLock excludes Close while one Enqueue is handing a batch over, so an accepted batch is
	// never lost to the close. closed is guarded by it.
	sendLock  sync.RWMutex
	closed    bool
	closeOnce sync.Once

	// lock guards the recorded failure.
	lock sync.Mutex
	err  error
}

// NewResultWriter starts the writer of one Job run. The write function comes from validated Job
// execution settings, so a missing one is a programming error reported before any result is
// accepted.
func NewResultWriter[T any](ctx context.Context, write func(context.Context, []T) error) (*ResultWriter[T], error) {
	if write == nil {
		return nil, fmt.Errorf("new result writer failed, write function is missing")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	return &ResultWriter[T]{
		ctx:   ctx,
		write: write,
		sem:   make(chan struct{}, resultWriterConcurrency),
	}, nil
}

// Enqueue starts the write of one batch. It performs no I/O itself: it waits for a free writer
// slot, which is the backpressure point of the feed, and returns the recorded write error, which
// is what stops the caller's feed.
func (w *ResultWriter[T]) Enqueue(items ...T) error {
	// Exclude Close for the duration of this enqueue, so an accepted batch is never dropped by a
	// close that races it.
	w.sendLock.RLock()
	defer w.sendLock.RUnlock()
	if w.closed {
		return errors.Join(w.Failure(), errResultWriterClosed)
	}

	if len(items) > 0 {
		// Waiting here is what bounds concurrent writes; the results feed behind it is what
		// applies backpressure to ACP.
		w.sem <- struct{}{}
		w.writers.Add(1)
		go func() {
			defer func() { <-w.sem; w.writers.Done() }()
			w.persist(items)
		}()
	}

	return w.Failure()
}

// Close stops accepting results, waits for every write to finish, and returns the recorded
// failure as the run's terminal error. It is idempotent, so a runner can close its writer on every
// return path.
func (w *ResultWriter[T]) Close() error {
	w.closeOnce.Do(func() {
		w.sendLock.Lock()
		w.closed = true
		w.sendLock.Unlock()
		w.writers.Wait()
	})
	return w.Failure()
}

// Failure reports the first write error this writer observed.
func (w *ResultWriter[T]) Failure() error {
	w.lock.Lock()
	defer w.lock.Unlock()
	return w.err
}

// persist writes one batch and records the first failure. Later batches still attempt their own
// write, so a failure never discards the results the run already reported.
func (w *ResultWriter[T]) persist(items []T) {
	err := w.write(w.ctx, items)
	if err == nil {
		return
	}
	w.lock.Lock()
	if w.err == nil {
		w.err = err
	}
	w.lock.Unlock()
}
