package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sync"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/media/readplan"
)

// ReadOrderMaxItems bounds one read window and one execution page.
const ReadOrderMaxItems = 1024

// ReadOrderProvider is a read planner provider that can also derive member ranges from saved locators.
type ReadOrderProvider interface {
	readplan.Provider
	Ranges(ReadRequest) ([]readplan.Range, bool)
}

// OrderedReadSession supplies an optional provider and receives the attempt's planned order.
type OrderedReadSession interface {
	ReadOrder(context.Context) (ReadOrderProvider, string, error)
	// ReadOrderPlanned reports the effective order and reason after planning or its fallback.
	ReadOrderPlanned(entity.TapeReadOrder, string)
}

func comparePosition(a, b *entity.TapeBytePosition) int {
	if a.LogicalObject < b.LogicalObject || a.LogicalObject == b.LogicalObject && a.OffsetBytes < b.OffsetBytes {
		return -1
	}
	if a.LogicalObject == b.LogicalObject && a.OffsetBytes == b.OffsetBytes {
		return 0
	}
	return 1
}

type readBoundary struct{ opened, settled bool }

// ReadBatch waits for physical source boundaries while output publication remains independent.
type ReadBatch struct {
	lock       sync.Mutex
	boundaries map[int64]*readBoundary
	remaining  int
	done       chan struct{}
	failure    error
}

func NewReadBatch(ids []int64) (*ReadBatch, error) {
	// One bounded window owns every loaded ID, including explicitly skipped items.
	if len(ids) == 0 || len(ids) > ReadOrderMaxItems {
		return nil, errors.New("read window item count is outside its bound")
	}
	batch := &ReadBatch{boundaries: make(map[int64]*readBoundary, len(ids)), remaining: len(ids), done: make(chan struct{})}
	for _, id := range ids {
		if id <= 0 || batch.boundaries[id] != nil {
			return nil, errors.New("read window has an invalid or duplicate item ID")
		}
		batch.boundaries[id] = new(readBoundary)
	}
	return batch, nil
}

func (b *ReadBatch) WrapSource(id int64, source acp.ContentSource) (acp.ContentSource, error) {
	b.lock.Lock()
	defer b.lock.Unlock()
	if source == nil || b.boundaries[id] == nil {
		return nil, errors.New("read source does not belong to this window")
	}
	return &batchSource{batch: b, id: id, source: source}, nil
}

// AcknowledgeResult settles an unopened terminal result or local skip, and reports actual source entry.
func (b *ReadBatch) AcknowledgeResult(id int64) (bool, error) {
	b.lock.Lock()
	defer b.lock.Unlock()
	boundary := b.boundaries[id]
	if boundary == nil {
		return false, errors.New("read result does not belong to this window")
	}
	if boundary.opened && !boundary.settled {
		return true, errors.New("read result preceded its source boundary")
	}
	b.settle(boundary, nil)
	return boundary.opened, nil
}

func (b *ReadBatch) settle(boundary *readBoundary, err error) {
	if boundary.settled {
		return
	}
	boundary.settled = true
	b.remaining--
	if err != nil && b.failure == nil {
		b.failure = err
		close(b.done)
	} else if b.remaining == 0 && b.failure == nil {
		close(b.done)
	}
}

func (b *ReadBatch) WaitSources(ctx context.Context, hardStopped <-chan struct{}) error {
	// The existing ACP Wait remains authoritative for a fatal cause; this signal only releases the feed.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-hardStopped:
		return errors.New("ACP stopped before the read window settled")
	case <-b.done:
		b.lock.Lock()
		defer b.lock.Unlock()
		return b.failure
	}
}

type batchSource struct {
	batch  *ReadBatch
	id     int64
	source acp.ContentSource
}

func (s *batchSource) Open(ctx context.Context) (file fs.File, err error) {
	// Open failure releases the feeder; ACP still owns any acquired file and drains it before finalization.
	s.batch.lock.Lock()
	boundary := s.batch.boundaries[s.id]
	if boundary.opened || boundary.settled {
		s.batch.lock.Unlock()
		return nil, errors.New("read source was already delivered")
	}
	boundary.opened = true
	s.batch.lock.Unlock()
	defer func() {
		if value := recover(); value != nil {
			s.batch.lock.Lock()
			s.batch.settle(boundary, fmt.Errorf("read source Open panicked: %v", value))
			s.batch.lock.Unlock()
			panic(value)
		}
		if err != nil {
			s.batch.lock.Lock()
			s.batch.settle(boundary, err)
			s.batch.lock.Unlock()
		}
	}()

	// Preserve every acquired handle for ACP's sole Close attempt, including Open errors.
	file, err = s.source.Open(ctx)
	if err == nil && file == nil {
		err = errors.New("read source returned no reader")
	}
	if file != nil {
		file = &batchReader{File: file, source: s, observeErr: err}
	}
	return file, err
}

type batchReader struct {
	fs.File
	source     *batchSource
	observeErr error
	readErr    error
}

func (r *batchReader) Stat() (info fs.FileInfo, err error) {
	// Observation failures must stop another window even if the physical Close succeeds.
	defer func() {
		if value := recover(); value != nil {
			r.observeErr = errors.Join(r.observeErr, fmt.Errorf("read source Stat panicked: %v", value))
			panic(value)
		}
		r.observeErr = errors.Join(r.observeErr, err)
	}()
	return r.File.Stat()
}

func (r *batchReader) Xattrs() (attrs map[string][]byte, err error) {
	// Embedding fs.File alone would hide a concrete source's optional attribute capability.
	defer func() {
		if value := recover(); value != nil {
			r.observeErr = errors.Join(r.observeErr, fmt.Errorf("read source Xattrs panicked: %v", value))
			panic(value)
		}
		r.observeErr = errors.Join(r.observeErr, err)
	}()
	if source, ok := r.File.(acp.XattrReader); ok {
		return source.Xattrs()
	}
	return nil, nil
}

func (r *batchReader) Read(data []byte) (n int, err error) {
	// Source I/O failure still stops planning when an otherwise ordinary reader's Close succeeds.
	defer func() {
		if value := recover(); value != nil {
			r.readErr = fmt.Errorf("read source panicked: %v", value)
			panic(value)
		}
		if err != nil && !errors.Is(err, io.EOF) && r.readErr == nil {
			r.readErr = err
		}
	}()
	return r.File.Read(data)
}

func (r *batchReader) Close() (err error) {
	// Acknowledgement follows the actual Close, including a terminal panic; it never guesses a cursor.
	defer func() {
		value := recover()
		boundaryErr := errors.Join(r.observeErr, r.readErr, err)
		if value != nil {
			boundaryErr = errors.Join(boundaryErr, fmt.Errorf("read source Close panicked: %v", value))
		}
		r.source.batch.lock.Lock()
		r.source.batch.settle(r.source.batch.boundaries[r.source.id], boundaryErr)
		r.source.batch.lock.Unlock()
		if value != nil {
			panic(value)
		}
	}()
	return r.File.Close()
}
