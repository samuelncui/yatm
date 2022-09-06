package tapewriter

import (
	"io"
	"os"
	"sync"
	"syscall"
)

var (
	_ = io.WriteCloser(new(BlockWriter))
)

type BlockWriter struct {
	target    uintptr
	blockSize int
	buffer    chan []byte
	pool      sync.Pool
	closed    sync.WaitGroup

	current []byte
	off     int
}

func NewBlockWriter(tape *os.File, blockSize, bufferBlocks int) *BlockWriter {
	w := &BlockWriter{
		target:    tape.Fd(),
		blockSize: blockSize,
		buffer:    make(chan []byte, bufferBlocks),
		current:   make([]byte, blockSize),
		pool:      sync.Pool{New: func() interface{} { return make([]byte, blockSize) }},
	}

	w.closed.Add(1)
	go w.loop()
	return w
}

func (w *BlockWriter) Write(buf []byte) (int, error) {
	var n, cn int
	for len(buf) > 0 {
		cn = copy(w.current, buf)
		buf = buf[cn:]
		w.off += cn
		n += cn

		if w.off >= w.blockSize {
			w.buffer <- w.current
			w.current = w.pool.Get().([]byte)
		}
	}

	return n, nil
}

func (w *BlockWriter) Close() error {
	w.buffer <- w.current[:w.off]
	close(w.buffer)

	w.closed.Wait()
	return nil
}

func (w *BlockWriter) loop() {
	defer w.closed.Done()

	for {
		buf, ok := <-w.buffer
		if !ok {
			break
		}

		_, err := syscall.Write(int(w.target), buf)
		if err != nil {
			panic(err)
		}
	}
}
