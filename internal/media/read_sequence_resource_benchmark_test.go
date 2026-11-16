package media

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"iter"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/resource"
)

// readResourceCancel selects where one resource benchmark cancels planning.
type readResourceCancel uint8

const (
	readResourceComplete readResourceCancel = iota
	// readResourceCancelIngesting cancels while the selection is still being streamed into the index.
	readResourceCancelIngesting
	// readResourceCancelIndexed cancels after the complete index and the LOGICAL route scoring exist.
	readResourceCancelIndexed
)

// readResourceIngested is the streamed member that cancels an ingesting attempt, after several index batches.
const readResourceIngested = 4096

// readResourceProvider counts production tar evaluations and cancels once the search follows LOGICAL scoring.
type readResourceProvider struct {
	*readScenarioObservedProvider
	cancelAfter uint64
	cancel      context.CancelFunc
}

func (p *readResourceProvider) Evaluate(state readplan.State, interval readplan.Range) (readplan.Transition, bool) {
	if p.cancel != nil && p.evaluations == p.cancelAfter {
		p.cancel()
	}
	return p.readScenarioObservedProvider.Evaluate(state, interval)
}

// readResourceInput streams count one-record tar members without holding the selection in memory.
// Members ascend along the nominal cartridge, as one Archive writes them: index inserts stay sequential.
func readResourceInput(count int) (iter.Seq2[readplan.Span, error], uint64) {
	const spacing = 8
	return func(yield func(readplan.Span, error) bool) {
		for i := range count {
			object := 2 + uint64(i)*spacing
			span := readplan.Span{ID: int64(i + 1), Ranges: []readplan.Range{{
				Start:  &entity.TapeBytePosition{LogicalObject: object},
				End:    &entity.TapeBytePosition{LogicalObject: object + 1},
				Stream: tarPrefix(1),
			}}}
			if !yield(span, nil) {
				return
			}
		}
	}, 2 + uint64(count)*spacing
}

// BenchmarkReadSequenceBudgetResources measures one budgeted attempt on a large streamed selection,
// complete consumption of the retained order and cleanup of the disposable index.
func BenchmarkReadSequenceBudgetResources(b *testing.B) {
	for _, count := range []int{100000, 1000000} {
		b.Run(fmt.Sprintf("Items%dBudget20s", count), func(b *testing.B) {
			benchmarkReadSequenceResource(b, count, 20*time.Second, readResourceComplete)
		})
	}
}

// BenchmarkReadSequenceCancellationResources cancels while a large selection is still being ingested.
func BenchmarkReadSequenceCancellationResources(b *testing.B) {
	benchmarkReadSequenceResource(b, 100000, 0, readResourceCancelIngesting)
}

// BenchmarkReadSequenceIndexedCancellationResources cancels after the complete index and route state exist.
func BenchmarkReadSequenceIndexedCancellationResources(b *testing.B) {
	for _, count := range []int{100000, 1000000} {
		b.Run(fmt.Sprintf("Items%d", count), func(b *testing.B) {
			benchmarkReadSequenceResource(b, count, 0, readResourceCancelIndexed)
		})
	}
}

// BenchmarkReadSequenceTarEvaluator1000 plans a small selection with the production tar evaluator.
func BenchmarkReadSequenceTarEvaluator1000(b *testing.B) {
	benchmarkReadSequenceResource(b, 1000, 15*time.Second, readResourceComplete)
}

func BenchmarkReadSequenceTarEvaluator10000(b *testing.B) {
	benchmarkReadSequenceResource(b, 10000, 15*time.Second, readResourceComplete)
}

func benchmarkReadSequenceResource(b *testing.B, count int, budget time.Duration, cancelAt readResourceCancel) {
	b.Helper()
	root := b.TempDir()
	input, objects := readResourceInput(count)
	var planning, consumeCleanup time.Duration
	var evaluations uint64
	var peak int64
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		ctx, cancel := context.WithCancel(context.Background())
		tarProvider := restoreWorkloadProvider(b, objects)
		tarProvider.session.options.ReadPlanningBudget = budget
		provider := &readResourceProvider{readScenarioObservedProvider: &readScenarioObservedProvider{ReadOrderProvider: tarProvider}}
		if cancelAt == readResourceCancelIndexed {
			// Scoring the LOGICAL route evaluates every member once; the next call belongs to the search.
			provider.cancel, provider.cancelAfter = cancel, uint64(count)
		}
		yielded := 0
		streamed := func(yield func(readplan.Span, error) bool) {
			for span, err := range input {
				if yielded++; cancelAt == readResourceCancelIngesting && yielded == readResourceIngested {
					cancel()
				}
				if !yield(span, err) {
					return
				}
			}
		}

		// Sampling covers the disposable planning directory, including SQLite journals; it may miss short peaks.
		sample := func() {
			total := int64(0)
			_ = filepath.WalkDir(resource.TemporaryRoot(root), func(_ string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return nil
				}
				if info, err := entry.Info(); err == nil {
					total += info.Size()
				}
				return nil
			})
			peak = max(peak, total)
		}
		stop, done := make(chan struct{}), make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					sample()
				}
			}
		}()

		// A canceled attempt removes its index before Plan returns, so planning time includes that cleanup.
		started := time.Now()
		sequence, err := readplan.Plan(ctx, root, provider, streamed)
		planned := time.Now()
		close(stop)
		<-done
		sample()
		canceled := ctx.Err() != nil
		cancel()
		switch {
		case cancelAt != readResourceComplete:
			if !canceled || !errors.Is(err, context.Canceled) || sequence != nil {
				b.Fatalf("planning ended before its cancellation boundary: canceled=%t err=%v", canceled, err)
			}
		case errors.Is(err, readplan.ErrBudget):
			// The budget expired during ingestion: the caller keeps its own manifest and no index remains.
			b.Logf("items=%d reason=%q", count, err)
		case err != nil:
			b.Fatal(err)
		default:
			// Consume the whole retained order, as execution does, and require every identity exactly once.
			seen, consumed := make([]uint64, count/64+1), 0
			for {
				ids, err := sequence.Next(context.Background(), 256)
				if err != nil {
					b.Fatal(err)
				}
				if len(ids) == 0 {
					break
				}
				for _, id := range ids {
					if id < 1 || id > int64(count) || seen[(id-1)/64]&(1<<uint((id-1)%64)) != 0 {
						b.Fatalf("read order returned invalid or repeated identity %d", id)
					}
					seen[(id-1)/64] |= 1 << uint((id-1)%64)
					consumed++
				}
			}
			if consumed != count {
				b.Fatalf("consumed %d selected identities, want %d", consumed, count)
			}
			logical, retained, _ := sequence.Estimates()
			b.Logf("items=%d reason=%q logical=%v retained=%v", count, sequence.Reason(), logical, retained)
			if err := sequence.Close(); err != nil {
				b.Fatal(err)
			}
		}
		entries, err := os.ReadDir(resource.TemporaryRoot(root))
		if err != nil && !os.IsNotExist(err) {
			b.Fatal(err)
		}
		if len(entries) != 0 {
			b.Fatalf("planning retained %d temporary resources", len(entries))
		}
		planning += planned.Sub(started)
		consumeCleanup += time.Since(planned)
		evaluations += provider.evaluations
	}
	b.StopTimer()
	b.ReportMetric(float64(planning)/float64(b.N), "plan-ns/op")
	b.ReportMetric(float64(consumeCleanup)/float64(b.N), "consume-cleanup-ns/op")
	b.ReportMetric(float64(evaluations)/float64(b.N), "evaluations/op")
	b.ReportMetric(float64(peak), "sampled-peak-temp-bytes")
	reportReadSequenceRSS(b)
}
