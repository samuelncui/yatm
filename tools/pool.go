package tools

import "sync"

type Pool[T any] struct {
	inner sync.Pool
}

func NewPool[T any](f func() T) *Pool[T] {
	pool := &Pool[T]{
		inner: sync.Pool{
			New: func() interface{} { return f() },
		},
	}

	return pool
}

func (p *Pool[T]) Get() T {
	return p.inner.Get().(T)
}

func (p *Pool[T]) Put(value T) {
	p.inner.Put(value)
}
