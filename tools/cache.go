package tools

func Cache[i comparable, o any](f func(in i) o) func(in i) o {
	cache := make(map[i]o, 0)
	return func(in i) o {
		cached, has := cache[in]
		if has {
			return cached
		}

		out := f(in)
		cache[in] = out
		return out
	}
}
