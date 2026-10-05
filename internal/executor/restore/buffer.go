package restore

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/media"
)

// copyResult is one finished ACP item waiting for the writer.
type copyResult struct {
	job    *copyJob
	result acp.Result
}

// copyBuffer is the result path of one Media attempt. It is the ACP results callback: ACP delivers
// a batch of results from one goroutine, the buffer hands that batch to the writer without doing
// any I/O of its own, and the shared writer completes and stages it outside the feed. A failed persistence
// is the writer's terminal error, which stops the run exactly like a caller cancellation: the
// copies already past the read stage are still reported, and the rest keep their PENDING status.
type copyBuffer struct {
	runner  *jobRestoreRunner
	mediaID int64
	session media.ReadSession
	writer  *executor.ResultWriter[copyResult]
	lock    sync.Mutex
	failure error
}

// newCopyBuffer opens the result path of one Media attempt with the operator's write limits.
func newCopyBuffer(
	ctx context.Context,
	runner *jobRestoreRunner,
	mediaID int64,
	session media.ReadSession,
) (*copyBuffer, error) {
	buffer := &copyBuffer{runner: runner, mediaID: mediaID, session: session}
	// Persistence survives the operator's stop; the attempt's own context must not cut it short.
	writer, err := executor.NewResultWriter(context.WithoutCancel(ctx), buffer.writeBatch)
	if err != nil {
		return nil, err
	}
	buffer.writer = writer
	return buffer, nil
}

// onResults hands one batch of results to the writer. A result that carries an error arrives on its
// own, so failures are classified as they arrive and successes stay in the batch they arrived in.
func (b *copyBuffer) onResults(results []acp.Result) error {
	batch := make([]copyResult, 0, len(results))
	for _, result := range results {
		job, ok := result.Job.(*copyJob)
		if !ok {
			return fmt.Errorf("restore copy result carries an unknown item")
		}
		batch = append(batch, copyResult{job: job, result: result})
	}
	return b.writer.Enqueue(batch...)
}

// writeBatch completes and stages one batch of finished items through the runner's transfer path.
// Every item keeps its own outcome: a failed item or a failed completion is recorded as the run's
// error while the rest of the batch is still persisted, so a failed run never loses the results it
// already reported.
func (b *copyBuffer) writeBatch(ctx context.Context, batch []copyResult) error {
	var failure error
	for _, item := range batch {
		err := b.runner.storeCompletion(ctx, b.mediaID, b.session, item.job, item.result)
		var local *restoreItemError
		if errors.As(err, &local) {
			if recordErr := b.runner.recordItemFailure(ctx, item.job.copy, err); recordErr != nil {
				err = errors.Join(err, recordErr)
			} else {
				b.lock.Lock()
				if b.failure == nil {
					b.failure = err
				}
				b.lock.Unlock()
				continue
			}
		}
		if err != nil && failure == nil {
			failure = err
		}
	}
	return failure
}

// Close drains the results the attempt accepted, waits for every write, and reports the terminal
// error the runner fails the attempt with.
func (b *copyBuffer) Close() error {
	err := b.writer.Close()
	b.lock.Lock()
	defer b.lock.Unlock()
	return errors.Join(err, b.failure)
}
