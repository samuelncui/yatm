package readplan

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"google.golang.org/protobuf/proto"
)

type link struct{ previous, next int64 }

// route is a doubly linked permutation of ranks; index zero is both the prepared origin and the finish.
type route []link

func (r route) clone() route { return append(route(nil), r...) }

func (r route) move(first, last, after int64) {
	// Remove and insert the directed run without changing any internal member order.
	before, next := r[first].previous, r[last].next
	r[before].next = next
	r[next].previous = before
	successor := r[after].next
	r[after].next = first
	r[first].previous = after
	r[last].next = successor
	r[successor].previous = last
}

func sameState(a, b State) bool {
	return a.Stream == b.Stream && proto.Equal(a.Position, b.Position) && proto.Equal(a.Cache, b.Cache)
}

// evaluate prices one complete member from state, visiting each extent once with its real cache/cursor semantics.
func (s *Sequence) evaluate(state State, span Span) (time.Duration, State, bool) {
	total := time.Duration(0)
	for _, interval := range span.Ranges {
		transition, known := s.provider.Evaluate(state, interval)
		if !known || transition.Next.Position == nil || transition.Cost < 0 {
			return 0, State{}, false
		}
		total += transition.Cost
		state = transition.Next
	}
	return total, state, true
}

// edge prices a member's certified exit plus the complete target read. Rank zero is the prepared
// origin as a source and the provider's finish action as a target; the fixed first member is unpriced.
func (s *Sequence) edge(ctx context.Context, from, to int64, initial State, c *cache) (time.Duration, bool, error) {
	key := [2]int64{from, to}
	if cached, ok := c.edges[key]; ok {
		return cached.cost, cached.known, nil
	}
	state := initial
	if s.fixedFirst != nil && from == 1 {
		state = *s.fixedFirst
	} else if from != 0 {
		value, err := c.get(ctx, from)
		if err != nil {
			return 0, false, err
		}
		state = value.canonical
	}

	var value edgeCost
	switch {
	case to == 0:
		cost, known := s.provider.Finish(state)
		value = edgeCost{cost: cost, known: known && cost >= 0}
	case s.fixedFirst != nil && to == 1:
		value = edgeCost{known: from == 0}
	default:
		target, err := c.get(ctx, to)
		if err != nil {
			return 0, false, err
		}
		cost, exit, known := s.evaluate(state, target.span)
		// A disagreeing exit would invalidate every edge priced from the target's certified state.
		value = edgeCost{cost: cost, known: known && sameState(exit, target.canonical)}
	}
	c.putEdge(key, value)
	return value.cost, value.known, nil
}

// score proves exact rank coverage and sums every directed edge, including origin and finish.
func (s *Sequence) score(ctx context.Context, r route, initial State, c *cache) (time.Duration, bool, error) {
	if s.fixedFirst != nil && r[0].next != 1 {
		return 0, false, fmt.Errorf("%w: the route changed its unpriced first member", errInconsistent)
	}
	seen := make([]bool, len(r))
	total, from, count := time.Duration(0), int64(0), 0
	for rank := r[0].next; rank != 0; rank = r[rank].next {
		if err := s.check(ctx); err != nil {
			return 0, false, err
		}
		if rank < 1 || rank >= int64(len(r)) || seen[rank] {
			return 0, false, fmt.Errorf("%w: the route has an invalid or duplicate rank", errInconsistent)
		}
		seen[rank] = true
		count++
		cost, known, err := s.edge(ctx, from, rank, initial, c)
		if err != nil || !known {
			return 0, false, err
		}
		total += cost
		from = rank
	}
	if count != len(r)-1 {
		return 0, false, fmt.Errorf("%w: the route does not cover the complete selection", errInconsistent)
	}
	cost, known, err := s.edge(ctx, from, 0, initial, c)
	return total + cost, known, err
}

func (s *Sequence) optimize(ctx context.Context, initial State, count int64) (rerr error) {
	// Compact links retain only permutation metadata; extents stay in SQLite behind a bounded cache.
	r := make(route, count+1)
	for rank := int64(1); rank <= count; rank++ {
		r[rank] = link{previous: rank - 1, next: rank + 1}
	}
	r[count].next = 0
	r[0] = link{previous: count, next: 1}
	statements := newStatements(s.stage.DB.Statement.ConnPool)
	defer func() { rerr = errors.Join(rerr, statements.Close()) }()
	c := newCache(statements)

	// The complete LOGICAL route is the first verified incumbent.
	total, known, err := s.score(ctx, r, initial, c)
	if err != nil {
		return s.stop(err)
	}
	if !known {
		s.reason = reasonCosts
		return nil
	}
	s.reason = reasonNoGain
	s.priced, s.logicalCost, s.plannedCost = true, total, total

	// Directed descent, then reconnection at a local minimum; bounded kicks explore other minima
	// from the best route without publishing a provisional cost increase.
	retained, best := total, r.clone()
	random := rand.New(rand.NewPCG(1, 2))
	trials, stagnant := 0, 0
	d := &descent{sequence: s, initial: initial, cache: c, marks: make([]uint32, len(r))}
	descending := false
	for round := 0; ; round++ {
		// Planning precedes every read. Search returns diminish, so once planning has taken longer
		// than the predicted saving achieved so far, further search cannot be expected to pay back.
		if round > 0 && s.now().Sub(s.started) > s.logicalCost-retained {
			return nil
		}
		predicted, improved, err := d.sweep(ctx, r, total, descending)
		if err != nil {
			return s.stop(err)
		}
		if !improved {
			predicted, improved, err = s.reconnect(ctx, r, initial, c, total)
			if err != nil {
				return s.stop(err)
			}
		}
		if !improved {
			if trials >= 12 || stagnant >= 4 {
				return nil
			}
			r = best.clone()
			changed, err := s.perturb(ctx, r, initial, c, random)
			if err != nil || !changed {
				return s.stop(err)
			}
			trials++
			stagnant++
			cost, known, err := s.score(ctx, r, initial, c)
			if err != nil {
				return s.stop(err)
			}
			if !known {
				// A kick that introduces an unknown edge is abandoned like any other unpriced candidate.
				r, total = best.clone(), retained
				continue
			}
			total = cost
			continue
		}

		// Changed-edge deltas must equal the complete traversal that authorizes a checkpoint.
		complete, err := s.confirm(ctx, r, initial, c, predicted, total)
		if err != nil {
			return s.stop(err)
		}
		total = complete
		descending = !descending
		if complete >= retained {
			continue
		}
		if err := s.commit(ctx, r); err != nil {
			return s.stop(err)
		}
		retained, stagnant = complete, 0
		s.plannedCost = complete
		best = append(best[:0], r...)
		s.planned, s.reason = true, reasonPlanned
		if s.fixedFirst != nil {
			s.reason += " after the fixed first LOGICAL member"
		}
	}
}

// confirm scores a delta-predicted route completely; a disagreement breaks a route invariant.
func (s *Sequence) confirm(ctx context.Context, r route, initial State, c *cache, predicted, total time.Duration,
) (time.Duration, error) {
	complete, known, err := s.score(ctx, r, initial, c)
	if err != nil {
		return 0, err
	}
	if !known || complete != predicted || complete >= total {
		return 0, fmt.Errorf("%w: a changed-edge estimate disagrees with its complete score", errInconsistent)
	}
	return complete, nil
}

// stop retains the last committed checkpoint or LOGICAL after budget expiry and withdraws every
// route after a broken invariant, keeping LOGICAL with that reason; other errors remain fatal.
func (s *Sequence) stop(err error) error {
	switch {
	case errors.Is(err, errInconsistent):
		s.planned, s.plannedCost = false, s.logicalCost
		s.reason = "LOGICAL: " + err.Error()
	case !errors.Is(err, ErrBudget):
		return err
	case !s.planned:
		s.reason = reasonBudget
	}
	return nil
}

// descent owns bounded scratch for one frozen search.
type descent struct {
	sequence *Sequence
	initial  State
	cache    *cache
	marks    []uint32
	epoch    uint32
	targets  []int64
}

// sweep reinserts single members, short runs and natural runs at their cheapest directed position.
// Unknown candidate edges skip only that candidate; the current route's edges are always known.
func (d *descent) sweep(ctx context.Context, r route, total time.Duration, descending bool) (time.Duration, bool, error) {
	s, c := d.sequence, d.cache
	edge := func(from, to int64) (time.Duration, bool, error) { return s.edge(ctx, from, to, d.initial, c) }
	current := func(from, to int64) (time.Duration, error) {
		cost, known, err := edge(from, to)
		if err == nil && !known {
			err = fmt.Errorf("%w: the route lost a priced edge", errInconsistent)
		}
		return cost, err
	}
	predicted, improved := total, false
	for visit := int64(1); visit < int64(len(r)); visit++ {
		first := visit
		if descending {
			first = int64(len(r)) - visit
		}
		if s.fixedFirst != nil && first == 1 {
			continue
		}
		if err := s.check(ctx); err != nil {
			return 0, false, err
		}
		for _, length := range [4]int{1, 2, 3, 0} {
			last, err := d.segment(ctx, r, first, length)
			if err != nil {
				return 0, false, err
			}
			if last == 0 {
				continue
			}

			// Price the unchanged side once: both detached edges and the bridge that closes the gap.
			before, next := r[first].previous, r[last].next
			bridge, known, err := edge(before, next)
			if err != nil {
				return 0, false, err
			}
			if !known {
				continue
			}
			entry, err := current(before, first)
			if err != nil {
				return 0, false, err
			}
			exit, err := current(last, next)
			if err != nil {
				return 0, false, err
			}
			detached := predicted - entry - exit + bridge

			// Compare directed insertions using complete target costs; ties keep the current route.
			d.mark(r, first, last)
			d.targets, err = s.insertionTargets(ctx, r, first, last, c, d.targets)
			if err != nil {
				return 0, false, err
			}
			bestAfter, bestCost := int64(-1), predicted
			for _, after := range d.targets {
				if s.fixedFirst != nil && after == 0 || d.marks[after] == d.epoch || after == before {
					continue
				}
				successor := r[after].next
				opened, err := current(after, successor)
				if err != nil {
					return 0, false, err
				}
				in, known, err := edge(after, first)
				if err != nil {
					return 0, false, err
				}
				out, outKnown, err := edge(last, successor)
				if err != nil {
					return 0, false, err
				}
				if !known || !outKnown {
					continue
				}
				if cost := detached - opened + in + out; cost < bestCost {
					bestAfter, bestCost = after, cost
				}
			}
			if bestAfter >= 0 {
				r.move(first, last, bestAfter)
				predicted, improved = bestCost, true
			}
		}
	}
	return predicted, improved, nil
}

// segment returns the last rank of a contiguous candidate starting at first, or zero when none applies.
// Length zero selects a natural run of more than one member that starts at first.
func (d *descent) segment(ctx context.Context, r route, first int64, length int) (int64, error) {
	if length == 0 {
		last, err := d.sequence.segmentEnd(ctx, r, first, d.cache)
		if err != nil || last == first {
			return 0, err
		}
		return last, nil
	}
	last := first
	for step := 1; step < length && last != 0; step++ {
		last = r[last].next
	}
	return last, nil
}

func (d *descent) mark(r route, first, last int64) {
	d.epoch++
	if d.epoch == 0 {
		clear(d.marks)
		d.epoch = 1
	}
	for rank := first; ; rank = r[rank].next {
		d.marks[rank] = d.epoch
		if rank == last {
			return
		}
	}
}
