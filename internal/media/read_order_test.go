package media

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
)

func readOrderPosition(object uint64) *entity.TapeBytePosition {
	return &entity.TapeBytePosition{LogicalObject: object}
}

func readOrderSpan(id int64, start, end uint64) readplan.Span {
	return readplan.Span{ID: id, Ranges: []readplan.Range{{Start: readOrderPosition(start), End: readOrderPosition(end)}}}
}

type batchTestSource func(context.Context) (fs.File, error)

func (s batchTestSource) Open(ctx context.Context) (fs.File, error) {
	return s(ctx)
}

type batchTestReader struct {
	io.Reader
	close    func() error
	statErr  error
	attrs    map[string][]byte
	attrsErr error
}

type failedBatchReader struct{ failure error }

func (r *failedBatchReader) Read([]byte) (int, error) { return 0, r.failure }
func (*failedBatchReader) Close() error               { return nil }
func (*failedBatchReader) Stat() (fs.FileInfo, error) { return tarTestInfo{name: "file", size: 1}, nil }

func (r *batchTestReader) Close() error { return r.close() }
func (r *batchTestReader) Stat() (fs.FileInfo, error) {
	return tarTestInfo{name: "file", size: 1}, r.statErr
}
func (r *batchTestReader) Xattrs() (map[string][]byte, error) { return r.attrs, r.attrsErr }

func TestReadBatchWaitsForActualCloseAndTracksUnopenedResults(t *testing.T) {
	// Source completion is independent of result delivery or native output publication.
	batch, err := NewReadBatch([]int64{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	source, err := batch.WrapSource(1, batchTestSource(func(context.Context) (fs.File, error) {
		return &batchTestReader{Reader: strings.NewReader("x"), close: func() error { close(entered); <-release; return nil }}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	reader, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := batch.AcknowledgeResult(1); err == nil {
		t.Fatal("result acknowledged an active source")
	}
	for _, id := range []int64{2, 3, 3} {
		if opened, err := batch.AcknowledgeResult(id); err != nil || opened {
			t.Fatalf("unopened id=%d entered=%t err=%v", id, opened, err)
		}
	}

	// A blocked Close cannot authorize the next position query; repeated terminal acknowledgements are harmless.
	done := make(chan error, 1)
	go func() { done <- reader.Close() }()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := batch.WaitSources(ctx, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked Close wait=%v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := batch.WaitSources(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if opened, err := batch.AcknowledgeResult(1); err != nil || !opened {
		t.Fatalf("closed source entered=%t err=%v", opened, err)
	}
}

func TestReadBatchFailureAndFatalStopReleaseFeed(t *testing.T) {
	// A failed source and a fatal engine both release the page feeder without inventing remaining source results.
	failure := errors.New("source Open failure")
	batch, _ := NewReadBatch([]int64{1, 2})
	source, _ := batch.WrapSource(1, batchTestSource(func(context.Context) (fs.File, error) {
		return nil, failure
	}))
	_, _ = source.Open(context.Background())
	if err := batch.WaitSources(context.Background(), nil); !errors.Is(err, failure) {
		t.Fatalf("source failure=%v", err)
	}
	batch, _ = NewReadBatch([]int64{1})
	hardStopped := make(chan struct{})
	close(hardStopped)
	if err := batch.WaitSources(context.Background(), hardStopped); err == nil {
		t.Fatal("fatal stop did not release source wait")
	}
}

func TestReadBatchReadFailureCannotAcknowledgeEstimatedEnd(t *testing.T) {
	// A non-tar source may close successfully after I/O failure; the shared boundary still stops the next window.
	failure := errors.New("physical source read failed")
	batch, _ := NewReadBatch([]int64{1})
	source, _ := batch.WrapSource(1, batchTestSource(func(context.Context) (fs.File, error) {
		return &failedBatchReader{failure: failure}, nil
	}))
	reader, err := source.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = reader.Read(make([]byte, 1))
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := batch.WaitSources(context.Background(), nil); !errors.Is(err, failure) {
		t.Fatalf("read boundary lost its failure: %v", err)
	}
}

func TestReadBatchObservationFailuresRetainCloseFailure(t *testing.T) {
	closeFailure := errors.New("source Close failed")
	for _, test := range []struct {
		name, method string
		closeErr     error
	}{
		{name: "Stat", method: "Stat"},
		{name: "Xattrs", method: "Xattrs"},
		{name: "Stat and Close", method: "Stat", closeErr: closeFailure},
		{name: "Xattrs and Close", method: "Xattrs", closeErr: closeFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A failed observation stops planning even when the physical Close succeeds.
			failure := errors.New("source observation failed")
			reader := &batchTestReader{close: func() error { return test.closeErr }}
			if test.method == "Stat" {
				reader.statErr = failure
			} else {
				reader.attrsErr = failure
			}
			batch, _ := NewReadBatch([]int64{1})
			source, _ := batch.WrapSource(1, batchTestSource(func(context.Context) (fs.File, error) { return reader, nil }))
			file, err := source.Open(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if test.method == "Stat" {
				_, err = file.Stat()
			} else {
				_, err = file.(acp.XattrReader).Xattrs()
			}
			if !errors.Is(err, failure) {
				t.Fatalf("observation failure=%v", err)
			}

			// Source settlement still follows actual Close, then reports both failure causes.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			if err := batch.WaitSources(ctx, nil); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("observation settled before Close: %v", err)
			}
			if err := file.Close(); !errors.Is(err, test.closeErr) {
				t.Fatalf("Close failure=%v", err)
			}
			if err := batch.WaitSources(context.Background(), nil); !errors.Is(err, failure) || test.closeErr != nil && !errors.Is(err, test.closeErr) {
				t.Fatalf("source boundary lost observation/Close failures: %v", err)
			}
		})
	}
}

func TestReadBatchOpenFailureReturnsAcquiredFileForDrain(t *testing.T) {
	// A failed Open releases the feeder but leaves the acquired file for ACP's authoritative drain.
	failure, closeFailure := errors.New("source Open failed"), errors.New("source Close failed")
	closes := 0
	batch, _ := NewReadBatch([]int64{1, 2})
	source, _ := batch.WrapSource(1, batchTestSource(func(context.Context) (fs.File, error) {
		return &batchTestReader{close: func() error { closes++; return closeFailure }}, failure
	}))
	file, err := source.Open(context.Background())
	if file == nil || !errors.Is(err, failure) || closes != 0 {
		t.Fatalf("file=%v err=%v closes=%d", file, err, closes)
	}
	if err := batch.WaitSources(context.Background(), nil); !errors.Is(err, failure) {
		t.Fatalf("early source failure=%v", err)
	}

	// The caller still performs the sole Close; an unopened remaining source is not acknowledged.
	if err := file.Close(); !errors.Is(err, closeFailure) || closes != 1 {
		t.Fatalf("drained Close=%v closes=%d", err, closes)
	}
	if opened, err := batch.AcknowledgeResult(1); err != nil || !opened || batch.remaining != 1 {
		t.Fatalf("opened=%t remaining=%d err=%v", opened, batch.remaining, err)
	}
}
