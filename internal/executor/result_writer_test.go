package executor

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// batchRecorder is a write function that records every batch it is asked to persist.
type batchRecorder struct {
	lock    sync.Mutex
	batches [][]int
}

func (r *batchRecorder) write(_ context.Context, batch []int) error {
	r.lock.Lock()
	r.batches = append(r.batches, append([]int(nil), batch...))
	r.lock.Unlock()
	return nil
}

func (r *batchRecorder) snapshot() [][]int {
	r.lock.Lock()
	defer r.lock.Unlock()
	return append([][]int(nil), r.batches...)
}

func TestResultWriterEnqueuePerformsNoIO(t *testing.T) {
	// The callback that owns the feed only starts a write: it returns while the writer is still
	// blocked inside a database write, so a slow backend never stalls the pipeline.
	ctx := context.Background()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var persisted atomic.Int64
	writer, err := NewResultWriter(ctx, func(_ context.Context, batch []int) error {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		persisted.Add(int64(len(batch)))
		return nil
	})
	require.NoError(t, err)

	enqueued := make(chan error, 1)
	go func() { enqueued <- writer.Enqueue(1, 2, 3, 4) }()
	<-started
	select {
	case err := <-enqueued:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("enqueue waited for the write instead of returning")
	}
	require.Zero(t, persisted.Load(), "enqueue returned before any write completed")

	close(release)
	require.NoError(t, writer.Close())
	require.EqualValues(t, 4, persisted.Load(), "Close waits for everything the caller accepted")
}

func TestResultWriterBlocksTheProducerWhileEveryWriterIsBusy(t *testing.T) {
	// The writer bound is the whole capacity of the result path: ACP's result queue is what holds
	// results meanwhile, so a longer burst waits here instead of growing memory.
	ctx := context.Background()
	release := make(chan struct{})
	writer, err := NewResultWriter(ctx, func(context.Context, []int) error {
		<-release
		return nil
	})
	require.NoError(t, err)

	blocked := make(chan struct{})
	go func() {
		defer close(blocked)
		for index := 0; index < resultWriterConcurrency+1; index++ {
			if err := writer.Enqueue(index); err != nil {
				return
			}
		}
	}()
	select {
	case <-blocked:
		t.Fatalf("the producer handed over more than %d batches without waiting", resultWriterConcurrency)
	case <-time.After(100 * time.Millisecond):
	}

	// Releasing the writers lets the producer hand over the rest.
	close(release)
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the producer stayed blocked after the writers were released")
	}
	require.NoError(t, writer.Close())
}

func TestResultWriterPersistsEveryAcceptedBatch(t *testing.T) {
	// The batches ACP hands over are persisted as they arrive, and every accepted item reaches the
	// database exactly once.
	ctx := context.Background()
	recorder := new(batchRecorder)
	writer, err := NewResultWriter(ctx, recorder.write)
	require.NoError(t, err)

	batches := [][]int{{1, 2, 3}, {4}, {}, {5, 6}}
	for _, batch := range batches {
		require.NoError(t, writer.Enqueue(batch...))
	}
	require.NoError(t, writer.Close())

	persisted := map[int]bool{}
	for _, batch := range recorder.snapshot() {
		require.NotEmpty(t, batch)
		for _, item := range batch {
			persisted[item] = true
		}
	}
	require.Equal(t, map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true}, persisted)
}

func TestResultWriterCapsConcurrentWriters(t *testing.T) {
	// The semaphore is what keeps one slow database from turning the feed into unbounded writes.
	ctx := context.Background()
	started := make(chan struct{}, 64)
	release := make(chan struct{})
	recorder := new(batchRecorder)
	writer, err := NewResultWriter(ctx, func(ctx context.Context, batch []int) error {
		started <- struct{}{}
		<-release
		return recorder.write(ctx, batch)
	})
	require.NoError(t, err)

	handed := make(chan struct{})
	go func() {
		defer close(handed)
		for index := 1; index <= resultWriterConcurrency+8; index++ {
			if err := writer.Enqueue(index); err != nil {
				return
			}
		}
	}()

	for index := 0; index < resultWriterConcurrency; index++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d writers started", index)
		}
	}
	select {
	case <-started:
		t.Fatalf("more than %d writers ran concurrently", resultWriterConcurrency)
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-handed:
	case <-time.After(5 * time.Second):
		t.Fatal("the producer stayed blocked after the writers were released")
	}
	require.NoError(t, writer.Close())
	persisted := 0
	for _, batch := range recorder.snapshot() {
		persisted += len(batch)
	}
	require.Equal(t, resultWriterConcurrency+8, persisted)
}

func TestResultWriterReportsTheFirstWriteErrorOnce(t *testing.T) {
	// The first failure is recorded once and returned by every later enqueue, which is how the run
	// stops; the writes already accepted still run, and a later failure never replaces it.
	ctx := context.Background()
	first := errors.New("job database is unavailable")
	later := errors.New("job database is still unavailable")
	writer, err := NewResultWriter(ctx, func(_ context.Context, batch []int) error {
		if batch[0] == 1 {
			return first
		}
		time.Sleep(50 * time.Millisecond)
		return later
	})
	require.NoError(t, err)

	// An enqueue never reports another error: only the recorded failure stops the feed.
	err = writer.Enqueue(1)
	require.True(t, err == nil || errors.Is(err, first), "enqueue reported %v", err)
	require.Eventually(t, func() bool { return writer.Failure() != nil }, 5*time.Second, time.Millisecond)
	require.ErrorIs(t, writer.Failure(), first)
	require.ErrorIs(t, writer.Enqueue(9), first, "the recorded error stops the feed")
	require.ErrorIs(t, writer.Close(), first, "the run reports the recorded failure")
	require.ErrorIs(t, writer.Failure(), first, "a later failure never replaces the recorded one")
}

func TestResultWriterCloseKeepsEveryReportedResult(t *testing.T) {
	// Close waits for every accepted batch, and one failed batch never discards the results the run
	// already reported.
	ctx := context.Background()
	failure := errors.New("write failed")
	recorder := new(batchRecorder)
	// Hold the injected failure until every batch is enqueued: an accepted batch still starts its
	// write, so the enqueues below must not race the failure they exist to precede.
	release := make(chan struct{})
	writer, err := NewResultWriter(ctx, func(ctx context.Context, batch []int) error {
		if batch[0] == 2 {
			<-release
			return failure
		}
		time.Sleep(time.Millisecond)
		return recorder.write(ctx, batch)
	})
	require.NoError(t, err)
	require.NoError(t, writer.Enqueue(1))
	require.NoError(t, writer.Enqueue(2))
	require.NoError(t, writer.Enqueue(3))
	require.NoError(t, writer.Enqueue(4))
	close(release)
	require.ErrorIs(t, writer.Close(), failure)

	persisted := map[int]bool{}
	for _, batch := range recorder.snapshot() {
		for _, item := range batch {
			persisted[item] = true
		}
	}
	require.Equal(t, map[int]bool{1: true, 3: true, 4: true}, persisted,
		"the writer persists every reported result it can, not only the ones before the failure")
}

func TestResultWriterRejectsAMissingWriteFunction(t *testing.T) {
	// The write function comes from validated Job execution settings, so an unusable configuration
	// is reported before any result is accepted.
	writer, err := NewResultWriter[int](context.Background(), nil)
	require.Error(t, err)
	require.Nil(t, writer)
}

func TestResultWriterCloseIsIdempotentAndRefusesLaterResults(t *testing.T) {
	ctx := context.Background()
	writer, err := NewResultWriter(ctx, func(context.Context, []int) error { return nil })
	require.NoError(t, err)

	require.NoError(t, writer.Close())
	// Close is idempotent, so a runner can close its writer on every return path.
	require.NoError(t, writer.Close())
	require.ErrorIs(t, writer.Enqueue(1), errResultWriterClosed, "a closed writer accepts no result")
	require.NoError(t, writer.Close())
}
