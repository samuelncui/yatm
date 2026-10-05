package executor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestResourceQueueAdmitsWaitersInArrivalOrder(t *testing.T) {
	q := newResourceQueue()
	require.NoError(t, q.acquire(context.Background(), "scan:1", nil))

	admitted := make(chan string, 2)
	var waiting sync.WaitGroup
	var lock sync.Mutex
	queued := 0
	enqueue := func(name string) {
		waiting.Add(1)
		go func() {
			defer waiting.Done()
			err := q.acquire(context.Background(), "scan:1", func() {
				lock.Lock()
				queued++
				lock.Unlock()
			})
			if err != nil {
				t.Errorf("acquire %s failed, %v", name, err)
				return
			}
			admitted <- name
		}()
	}
	queuedWaiters := func(count int) {
		require.Eventually(t, func() bool {
			lock.Lock()
			defer lock.Unlock()
			return queued == count
		}, 5*time.Second, time.Millisecond)
	}
	enqueue("second")
	queuedWaiters(1)
	enqueue("third")
	queuedWaiters(2)

	// Each release hands the resource to the waiter that arrived first.
	q.release("scan:1")
	require.Equal(t, "second", <-admitted)
	q.release("scan:1")
	require.Equal(t, "third", <-admitted)
	q.release("scan:1")
	waiting.Wait()
}

func TestResourceQueueDropsCanceledWaiterWithoutLosingTheResource(t *testing.T) {
	q := newResourceQueue()
	require.NoError(t, q.acquire(context.Background(), "scan:1", nil))

	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan struct{})
	abandoned := make(chan error, 1)
	go func() { abandoned <- q.acquire(ctx, "scan:1", func() { close(waited) }) }()
	<-waited
	cancel()
	require.ErrorIs(t, <-abandoned, context.Canceled)

	// The holder still owns the resource, and a later waiter receives it on release.
	admitted := make(chan struct{})
	go func() {
		if err := q.acquire(context.Background(), "scan:1", nil); err != nil {
			t.Errorf("later acquire failed, %v", err)
			return
		}
		close(admitted)
	}()
	require.Eventually(t, func() bool {
		q.lock.Lock()
		defer q.lock.Unlock()
		return len(q.waiters["scan:1"]) == 1
	}, 5*time.Second, time.Millisecond)
	q.release("scan:1")
	select {
	case <-admitted:
	case <-time.After(5 * time.Second):
		t.Fatal("later waiter never received the released resource")
	}
	q.release("scan:1")

	// A fully released key is free again.
	require.NoError(t, q.acquire(context.Background(), "scan:1", nil))
	q.release("scan:1")
}
