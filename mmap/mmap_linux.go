// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build linux
// +build linux

// Package mmap provides a way to memory-map a file.
package mmap

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"syscall"
)

const (
	prefetchMaxSize = 16 * 1024 * 1024
)

// debug is whether to print debugging messages for manual testing.
//
// The runtime.SetFinalizer documentation says that, "The finalizer for x is
// scheduled to run at some arbitrary time after x becomes unreachable. There
// is no guarantee that finalizers will run before a program exits", so we
// cannot automatically test that the finalizer runs. Instead, set this to true
// when running the manual test.
const debug = false

// ReaderAt reads a memory-mapped file.
//
// Like any io.ReaderAt, clients can execute parallel ReadAt calls, but it is
// not safe to call Close and reading methods concurrently.
type ReaderAt struct {
	data []byte
}

// Close closes the reader.
func (r *ReaderAt) Close() error {
	if r.data == nil {
		return nil
	}
	data := r.data
	r.data = nil
	if debug {
		var p *byte
		if len(data) != 0 {
			p = &data[0]
		}
		println("munmap", r, p)
	}
	runtime.SetFinalizer(r, nil)
	return syscall.Munmap(data)
}

// Len returns the length of the underlying memory-mapped file.
func (r *ReaderAt) Len() int {
	return len(r.data)
}

// At returns the byte at index i.
func (r *ReaderAt) At(i int) byte {
	return r.data[i]
}

// ReadAt implements the io.ReaderAt interface.
func (r *ReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if r.data == nil {
		return 0, errors.New("mmap: closed")
	}
	if off < 0 || int64(len(r.data)) < off {
		return 0, fmt.Errorf("mmap: invalid ReadAt offset %d", off)
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// ReadAt implements the io.ReaderAt interface.
func (r *ReaderAt) Slice(off, limit int64) ([]byte, error) {
	if r.data == nil {
		return nil, errors.New("mmap: closed")
	}

	l := int64(len(r.data))
	if off < 0 || limit < 0 || l < off {
		return nil, fmt.Errorf("mmap: invalid ReadAt offset %d", off)
	}

	if off+limit > l {
		return r.data[off:], nil
	}

	return r.data[off : off+limit], nil
}

// Open memory-maps the named file for reading.
func Open(filename string) (*ReaderAt, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	size := fi.Size()
	if size == 0 {
		return &ReaderAt{}, nil
	}
	if size < 0 {
		return nil, fmt.Errorf("mmap: file %q has negative size", filename)
	}
	if size != int64(int(size)) {
		return nil, fmt.Errorf("mmap: file %q is too large", filename)
	}

	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("create mmap fail, %q, %w", filename, err)
	}
	if size <= prefetchMaxSize {
		if err := syscall.Madvise(data, syscall.MADV_SEQUENTIAL|syscall.MADV_WILLNEED); err != nil {
			return nil, fmt.Errorf("madvise fail, %q, %w", filename, err)
		}
	}

	r := &ReaderAt{data}
	if debug {
		var p *byte
		if len(data) != 0 {
			p = &data[0]
		}
		println("mmap", r, p)
	}
	runtime.SetFinalizer(r, (*ReaderAt).Close)
	return r, nil
}
