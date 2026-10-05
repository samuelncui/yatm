package executor

import (
	"context"
	"fmt"
	"sync"
)

func tapeResource(device string) string {
	return fmt.Sprintf("tape:%s", device)
}

func volumeResource(uuid string) string {
	return fmt.Sprintf("volume:%s", uuid)
}

// resourceQueue serializes Tape devices and Volume leases. Contention waits instead of failing the
// Job, and waiters are admitted in arrival order so a long-running holder cannot starve an earlier request.
type resourceQueue struct {
	lock    sync.Mutex
	held    map[string]struct{}
	waiters map[string][]chan struct{}
}

func newResourceQueue() *resourceQueue {
	return &resourceQueue{held: make(map[string]struct{}), waiters: make(map[string][]chan struct{})}
}

// AcquireJobResource reserves one resource key until the returned release runs exactly once.
// onWait observes an actual wait, so callers can publish their waiting phase.
func (e *Executor) AcquireJobResource(ctx context.Context, key string, onWait func()) (func(), error) {
	if err := e.resources.acquire(ctx, key, onWait); err != nil {
		return nil, err
	}
	return func() { e.resources.release(key) }, nil
}

// tryAcquire reserves a free key without waiting, for operator-bound requests that report a busy
// resource instead of blocking their response.
func (e *Executor) tryAcquire(key string) (func(), bool) {
	e.resources.lock.Lock()
	defer e.resources.lock.Unlock()
	if _, busy := e.resources.held[key]; busy {
		return nil, false
	}
	e.resources.held[key] = struct{}{}
	return func() { e.resources.release(key) }, true
}

// resourceHeld reports whether any owner currently holds the key.
func (e *Executor) resourceHeld(key string) bool {
	e.resources.lock.Lock()
	defer e.resources.lock.Unlock()
	_, busy := e.resources.held[key]
	return busy
}

func (q *resourceQueue) acquire(ctx context.Context, key string, onWait func()) error {
	// A caller that is already canceled never takes or waits for the resource.
	if err := ctx.Err(); err != nil {
		return err
	}
	q.lock.Lock()
	if _, busy := q.held[key]; !busy {
		q.held[key] = struct{}{}
		q.lock.Unlock()
		return nil
	}
	ready := make(chan struct{})
	q.waiters[key] = append(q.waiters[key], ready)
	q.lock.Unlock()

	// Report the wait outside the queue lock; the caller may publish a waiting phase.
	if onWait != nil {
		onWait()
	}
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		q.lock.Lock()
		defer q.lock.Unlock()
		if dropWaiter(q.waiters, key, ready) {
			return ctx.Err()
		}
		// The slot was handed over while this caller gave up; pass it on instead of leaking it.
		q.handover(key)
		return ctx.Err()
	}
}

// release hands the key to the first waiter, or marks it free when nobody waits.
func (q *resourceQueue) release(key string) {
	q.lock.Lock()
	defer q.lock.Unlock()
	q.handover(key)
}

// handover transfers the key while the queue lock is held, so the resource is never briefly free.
func (q *resourceQueue) handover(key string) {
	waiting := q.waiters[key]
	if len(waiting) == 0 {
		delete(q.held, key)
		return
	}
	next := waiting[0]
	if len(waiting) == 1 {
		delete(q.waiters, key)
	} else {
		q.waiters[key] = waiting[1:]
	}
	close(next)
}

// dropWaiter removes one still-queued waiter and reports whether it was found.
func dropWaiter(waiters map[string][]chan struct{}, key string, ready chan struct{}) bool {
	waiting := waiters[key]
	for index, candidate := range waiting {
		if candidate != ready {
			continue
		}
		if len(waiting) == 1 {
			delete(waiters, key)
		} else {
			remaining := append(waiting[:index:index], waiting[index+1:]...)
			waiters[key] = remaining
		}
		return true
	}
	return false
}
