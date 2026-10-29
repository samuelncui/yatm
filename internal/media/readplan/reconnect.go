package readplan

import (
	"context"
	"slices"
	"time"
)

type readRun struct{ first, last int64 }

type readReconnection struct {
	cuts  [4]int
	order [4]int
	count int
	cost  time.Duration
}

type readRunCosts struct {
	sequence *Sequence
	runs     []readRun
	initial  State
	cache    *cache
	edges    map[[2]int]edgeCost
}

var readBlockOrders = func() [5][][4]int {
	// The finite permutations preserve every directed block's internal order.
	var result [5][][4]int
	for count := 2; count <= 4; count++ {
		var order [4]int
		var visit func(int, uint8)
		visit = func(at int, used uint8) {
			if at == count {
				identity := true
				for i := range count {
					identity = identity && order[i] == i
				}
				if !identity {
					result[count] = append(result[count], order)
				}
				return
			}
			for next := range count {
				if used&(1<<next) == 0 {
					order[at] = next
					visit(at+1, used|1<<next)
				}
			}
		}
		visit(0, 0)
	}
	return result
}()

func (s *Sequence) naturalRuns(ctx context.Context, route route, cache *cache) ([]readRun, error) {
	// Only compact endpoint ranks are kept in memory; extents stay in the existing bounded cache.
	var runs []readRun
	for first := route[0].next; first != 0; {
		last := first
		for next := route[last].next; next != 0; next = route[last].next {
			if err := s.check(ctx); err != nil {
				return nil, err
			}
			joined, err := s.joined(ctx, last, next, cache)
			if err != nil {
				return nil, err
			}
			if !joined {
				break
			}
			last = next
		}
		runs = append(runs, readRun{first: first, last: last})
		first = route[last].next
	}
	return runs, nil
}

func (c *readRunCosts) edge(ctx context.Context, from, to int) (time.Duration, bool, error) {
	// The key names run exits and entries; zero is the real prepared origin or modeled finish.
	key := [2]int{from, to}
	if edge, ok := c.edges[key]; ok {
		return edge.cost, edge.known, nil
	}
	before, next := int64(0), int64(0)
	if from > 0 {
		before = c.runs[from-1].last
	}
	if to > 0 {
		next = c.runs[to-1].first
	}
	cost, known, err := c.sequence.edge(ctx, before, next, c.initial, c.cache)
	if err != nil {
		return 0, false, err
	}

	// This cache is bounded independently of the number of files/runs; no all-pairs matrix is built.
	if len(c.edges) == 4096 {
		clear(c.edges)
	}
	c.edges[key] = edgeCost{cost: cost, known: known}
	return cost, known, nil
}

func (c *readRunCosts) consider(ctx context.Context, cuts [4]int, count int,
	total time.Duration, best *readReconnection,
) error {
	// Four internal cuts produce at most four directed blocks after the unchanged prefix.
	// Terminal edges are explicitly removed and inserted as well; completion is never free.
	var starts, ends [4]int
	removed := time.Duration(0)
	for i := range count {
		starts[i], ends[i] = cuts[i]+1, len(c.runs)
		if i+1 < count {
			ends[i] = cuts[i+1]
		}
		cost, known, err := c.edge(ctx, cuts[i], starts[i])
		if err != nil || !known {
			return err
		}
		removed += cost
	}
	finish, known, err := c.edge(ctx, len(c.runs), 0)
	if err != nil || !known {
		return err
	}
	removed += finish

	// At most four blocks share the same directed boundary prices across every permutation.
	// This fixed stack storage replaces repeated map lookups; it never scales with selection size.
	if err := c.sequence.check(ctx); err != nil {
		return err
	}
	var entry, exit [4]edgeCost
	var between [4][4]edgeCost
	for i := range count {
		cost, known, err := c.edge(ctx, cuts[0], starts[i])
		if err != nil {
			return err
		}
		entry[i] = edgeCost{cost: cost, known: known}
		cost, known, err = c.edge(ctx, ends[i], 0)
		if err != nil {
			return err
		}
		exit[i] = edgeCost{cost: cost, known: known}
		for j := range count {
			if i == j {
				continue
			}
			cost, known, err = c.edge(ctx, ends[i], starts[j])
			if err != nil {
				return err
			}
			between[i][j] = edgeCost{cost: cost, known: known}
		}
	}

	// Time bounds one batch of at most 23 permutations; cancellation remains checked per proposal.
	for _, order := range readBlockOrders[count] {
		if err := ctx.Err(); err != nil {
			return err
		}
		inserted, from, available := time.Duration(0), -1, true
		for i := range count {
			block := order[i]
			edge := entry[block]
			if from >= 0 {
				edge = between[from][block]
			}
			if !edge.known {
				available = false
				break
			}
			inserted += edge.cost
			from = block
		}
		if !available {
			continue
		}
		if !exit[from].known {
			continue
		}
		candidate := total - removed + inserted + exit[from].cost
		if candidate >= best.cost {
			continue
		}
		*best = readReconnection{cuts: cuts, order: order, count: count, cost: candidate}
	}
	return c.sequence.check(ctx)
}

func readCutCombinations(boundaries []int, visit func([4]int, int) error) error {
	// Generate combinations in stable order without retaining a combinatorial candidate list.
	var cuts [4]int
	var choose func(int, int, int) error
	choose = func(start, at, count int) error {
		if at == count {
			return visit(cuts, count)
		}
		for i := start; i <= len(boundaries)-(count-at); i++ {
			cuts[at] = boundaries[i]
			if err := choose(i+1, at+1, count); err != nil {
				return err
			}
		}
		return nil
	}
	for count := 2; count <= min(4, len(boundaries)); count++ {
		if err := choose(0, 0, count); err != nil {
			return err
		}
	}
	return nil
}

func (r readReconnection) apply(route route, runs []readRun) {
	// Rewire block boundaries only. File order, extent order and every internal directed link survive unchanged.
	from := int64(0)
	if r.cuts[0] > 0 {
		from = runs[r.cuts[0]-1].last
	}
	for i := range r.count {
		block := r.order[i]
		first, last := runs[r.cuts[block]].first, runs[len(runs)-1].last
		if block+1 < r.count {
			last = runs[r.cuts[block+1]-1].last
		}
		route[from].next = first
		route[first].previous = from
		from = last
	}
	route[from].next = 0
	route[0].previous = from
}

// reconnect exchanges up to four directed blocks between natural runs at a local minimum.
// It returns the exact changed-edge cost of the applied improvement.
func (s *Sequence) reconnect(ctx context.Context, route route, initial State, cache *cache,
	total time.Duration,
) (time.Duration, bool, error) {
	// Natural bonds guide this neighbourhood only; ordinary one/two/three-member moves can still split them.
	runs, err := s.naturalRuns(ctx, route, cache)
	if err != nil || len(runs) < 2 {
		return 0, false, err
	}
	minimum := 0
	if s.fixedFirst != nil {
		minimum = 1
	}
	costs := readRunCosts{sequence: s, runs: runs, initial: initial, cache: cache, edges: make(map[[2]int]edgeCost)}
	best := readReconnection{cost: total}
	visit := func(cuts [4]int, count int) error { return costs.consider(ctx, cuts, count, total, &best) }

	// Small run sets receive complete boundary coverage; larger ones use indexed run neighbourhoods.
	if len(runs) <= 32 {
		boundaries := make([]int, 0, len(runs))
		for i := minimum; i < len(runs); i++ {
			boundaries = append(boundaries, i)
		}
		if err := readCutCombinations(boundaries, visit); err != nil {
			return 0, false, err
		}
	} else if err := s.reconnectNearby(ctx, runs, cache, &costs, minimum, visit,
		func() bool { return best.count != 0 }); err != nil {
		return 0, false, err
	}
	if best.count == 0 {
		return 0, false, nil
	}
	best.apply(route, runs)
	return best.cost, true, nil
}

func readNearbyCuts(anchor, a, b, minimum, count int) []int {
	// Before/after boundaries of three distinct runs bound the large-selection neighbourhood.
	var storage [6]int
	boundaries := storage[:0]
	for _, run := range [3]int{anchor, a, b} {
		for _, boundary := range [2]int{run, run + 1} {
			if boundary >= minimum && boundary < count {
				boundaries = append(boundaries, boundary)
			}
		}
	}
	slices.Sort(boundaries)
	return slices.Compact(boundaries)
}

// segmentEnd returns the last member of the natural run starting at first, or zero when first continues a run.
// Only Cache/Continue bonds form a run; shorter segments inside it remain available for splitting.
func (s *Sequence) segmentEnd(ctx context.Context, r route, first int64, c *cache) (int64, error) {
	if before := r[first].previous; before != 0 {
		joined, err := s.joined(ctx, before, first, c)
		if err != nil || joined {
			return 0, err
		}
	}
	last := first
	for next := r[last].next; next != 0; next = r[last].next {
		if err := s.check(ctx); err != nil {
			return 0, err
		}
		joined, err := s.joined(ctx, last, next, c)
		if err != nil {
			return 0, err
		}
		if !joined {
			break
		}
		last = next
	}
	return last, nil
}

// joined reports whether next starts with a cached or continued read from before's certified exit.
func (s *Sequence) joined(ctx context.Context, before, next int64, c *cache) (bool, error) {
	a, err := c.get(ctx, before)
	if err != nil {
		return false, err
	}
	b, err := c.get(ctx, next)
	if err != nil {
		return false, err
	}
	state := a.canonical
	if s.fixedFirst != nil && before == 1 {
		state = *s.fixedFirst
	}
	transition, known := s.provider.Evaluate(state, b.span.Ranges[0])
	return known && (transition.Method == MethodCache || transition.Method == MethodContinue), nil
}
