package tools

import "sync"

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
