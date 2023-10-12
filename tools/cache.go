package tools

import (
	"context"
	"sync"
)

func Cache[K comparable, V any](f func(in K) V) func(in K) V {
	cache := new(sync.Map)
	return func(in K) V {
		cached, has := cache.Load(in)
		if has {
			return cached.(V)
		}

		out := f(in)
		cache.Store(in, out)
		return out
	}
}

func ThreadUnsafeCache[K comparable, V any](f func(in K) V) func(in K) V {
	cache := make(map[K]V, 32)
	return func(in K) V {
		cached, has := cache[in]
		if has {
			return cached
		}

		out := f(in)
		cache[in] = out
		return out
	}
}

type CacheOnce[K comparable, V any] struct {
	cached sync.Map
	getter func(context.Context, K) (V, error)
}

type cacheOnceItem[V any] struct {
	once  sync.Once
	value V
}

func NewCacheOnce[K comparable, V any](getter func(context.Context, K) (V, error)) *CacheOnce[K, V] {
	return &CacheOnce[K, V]{getter: getter}
}

func (c *CacheOnce[K, V]) Get(ctx context.Context, req K) (V, error) {
	if v, have := c.cached.Load(req); have {
		item, ok := v.(*cacheOnceItem[V])
		if ok {
			return c.get(ctx, item, req)
		}
	}

	v, _ := c.cached.LoadOrStore(req, &cacheOnceItem[V]{})
	return c.get(ctx, v.(*cacheOnceItem[V]), req)
}

func (c *CacheOnce[K, V]) Remove(req K) {
	c.cached.Delete(req)
}

func (c *CacheOnce[K, V]) get(ctx context.Context, item *cacheOnceItem[V], key K) (V, error) {
	var err error
	item.once.Do(func() {
		v, e := c.getter(ctx, key)
		if e != nil {
			err = e
			return
		}
		item.value = v
	})

	if err != nil {
		var v V
		return v, err
	}

	return item.value, nil
}
