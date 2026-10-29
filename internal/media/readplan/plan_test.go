package readplan

import (
	"context"
	"encoding/binary"
	"errors"
	"iter"
	"math/rand/v2"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
)

// lineProvider prices a one-dimensional Tape: seeking costs the distance, reading costs the length,
// and finishing rewinds to zero. Optional hooks withdraw individual facts.
type lineProvider struct {
	current   *uint64
	fixed     bool
	canonical bool
	indexed   bool
	unknown   func(from, start uint64) bool
	entries   []RangeEntry
	walkErr   error
}

func newLineProvider(current uint64) *lineProvider {
	return &lineProvider{current: &current, canonical: true, indexed: true}
}

func linePosition(object uint64) *entity.TapeBytePosition {
	return &entity.TapeBytePosition{LogicalObject: object}
}

func lineSpan(id int64, start, end uint64) Span {
	return Span{ID: id, Ranges: []Range{{Start: linePosition(start), End: linePosition(end)}}}
}

func (p *lineProvider) Current(context.Context) (State, bool, error) {
	if p.current == nil {
		return State{}, false, nil
	}
	return State{Position: linePosition(*p.current)}, true, nil
}

func (p *lineProvider) Evaluate(from State, to Range) (Transition, bool) {
	a, b := from.Position.LogicalObject, to.Start.LogicalObject
	if p.unknown != nil && p.unknown(a, b) {
		return Transition{}, false
	}
	distance := max(a, b) - min(a, b)
	method := MethodSeek
	if distance == 0 {
		method = MethodContinue
	}
	cost := time.Duration(distance+to.End.LogicalObject-b) * time.Millisecond
	return Transition{Cost: cost, Next: State{Position: linePosition(to.End.LogicalObject)}, Method: method}, true
}

func (p *lineProvider) CanonicalReadState(span Span) (State, bool) {
	return State{Position: linePosition(span.Ranges[len(span.Ranges)-1].End.LogicalObject)}, p.canonical
}

func (p *lineProvider) FixedFirstReadState(span Span) (State, bool) {
	state, _ := p.CanonicalReadState(span)
	return state, p.fixed
}

func (p *lineProvider) ReadIndex(position *entity.TapeBytePosition) (Index, bool) {
	return Index{Groups: [][]byte{{1}}, Position: binary.BigEndian.AppendUint64(nil, position.LogicalObject)}, p.indexed
}

func (p *lineProvider) Finish(state State) (time.Duration, bool) {
	return time.Duration(state.Position.LogicalObject) * time.Millisecond, true
}

func (p *lineProvider) ReadPlanningBudget() time.Duration { return 0 }

type indexedLineProvider struct{ *lineProvider }

func (p indexedLineProvider) WalkReadRanges(ctx context.Context, yield func(RangeEntry) error) error {
	if p.walkErr != nil {
		return p.walkErr
	}
	for _, entry := range p.entries {
		if err := yield(entry); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func lineInput(spans []Span) iter.Seq2[Span, error] {
	return func(yield func(Span, error) bool) {
		for _, span := range spans {
			if !yield(span, nil) {
				return
			}
		}
	}
}

// shuffledSpans places unit-length members at random positions in LOGICAL order.
func shuffledSpans(count int, seed uint64) []Span {
	random := rand.New(rand.NewPCG(seed, 1))
	spans := make([]Span, count)
	for i := range spans {
		start := uint64(random.IntN(10000))
		spans[i] = lineSpan(int64(i+1), start, start+1)
	}
	return spans
}

func fixedClock() func() time.Time {
	at := time.Unix(1, 0)
	return func() time.Time { return at }
}

func planIDs(t *testing.T, provider Provider, spans []Span, now func() time.Time) (*Sequence, []int64) {
	t.Helper()
	root := t.TempDir()
	sequence, err := plan(t.Context(), root, provider, lineInput(spans), now)
	require.NoError(t, err)
	var ids []int64
	for {
		page, err := sequence.Next(t.Context(), 7)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		ids = append(ids, page...)
	}
	require.NoError(t, sequence.Close())
	entries, err := os.ReadDir(resource.TemporaryRoot(root))
	require.True(t, err == nil || os.IsNotExist(err))
	require.Empty(t, entries, "the disposable index must not survive Close")
	return sequence, ids
}

// routeCost independently replays a delivered route from the prepared origin through the finish action.
func routeCost(t *testing.T, provider *lineProvider, spans []Span, ids []int64) time.Duration {
	t.Helper()
	byID := make(map[int64]Span, len(spans))
	for _, span := range spans {
		byID[span.ID] = span
	}
	state, _, _ := provider.Current(t.Context())
	total := time.Duration(0)
	for i, id := range ids {
		span := byID[id]
		if i == 0 && provider.current == nil {
			state, _ = provider.FixedFirstReadState(span)
			continue
		}
		for _, interval := range span.Ranges {
			transition, known := provider.Evaluate(state, interval)
			require.True(t, known)
			total += transition.Cost
			state = transition.Next
		}
	}
	finish, _ := provider.Finish(state)
	return total + finish
}

func logicalIDs(spans []Span) []int64 {
	ids := make([]int64, len(spans))
	for i, span := range spans {
		ids[i] = span.ID
	}
	return ids
}

func TestPlanKeepsEveryMemberAndNeverExceedsLogical(t *testing.T) {
	for _, count := range []int{2, 9, 40, 300} {
		spans := shuffledSpans(count, uint64(count))
		provider := newLineProvider(0)
		sequence, ids := planIDs(t, provider, spans, fixedClock())

		// Exactly the selected identities are delivered once, and the route is no costlier than LOGICAL.
		require.ElementsMatch(t, logicalIDs(spans), ids)
		require.LessOrEqual(t, routeCost(t, provider, spans, ids), routeCost(t, provider, spans, logicalIDs(spans)))
		if count > 2 {
			require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, sequence.Order(), sequence.Reason())
			require.Less(t, routeCost(t, provider, spans, ids), routeCost(t, provider, spans, logicalIDs(spans)))
		}
	}
}

func TestPlanIsDeterministicWithFixedClock(t *testing.T) {
	spans := shuffledSpans(200, 7)
	_, first := planIDs(t, newLineProvider(0), spans, fixedClock())
	_, second := planIDs(t, newLineProvider(0), spans, fixedClock())
	require.Equal(t, first, second)
}

func TestPlanBudgetExpiryKeepsLogicalOrCheckpoint(t *testing.T) {
	// The budget expires after construction; the complete LOGICAL manifest stays executable.
	spans := shuffledSpans(50, 3)
	calls, at := 0, time.Unix(1, 0)
	now := func() time.Time {
		calls++
		if calls > len(spans)+3 {
			return at.Add(DefaultBudget)
		}
		return at
	}
	sequence, ids := planIDs(t, newLineProvider(0), spans, now)
	require.Equal(t, reasonBudget, sequence.Reason())
	require.Equal(t, logicalIDs(spans), ids)

	// Expiry while the manifest is still being indexed asks the caller to keep its own order.
	_, err := plan(t.Context(), t.TempDir(), newLineProvider(0), lineInput(spans), func() time.Time {
		calls++
		return at.Add(time.Duration(calls) * time.Minute)
	})
	require.ErrorIs(t, err, ErrBudget)
}

func TestPlanCancellationIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := plan(ctx, t.TempDir(), newLineProvider(0), lineInput(shuffledSpans(10, 1)), fixedClock())
	require.ErrorIs(t, err, context.Canceled)
}

func TestPlanFixesFirstMemberWhenOriginIsUnknown(t *testing.T) {
	spans := shuffledSpans(60, 11)
	provider := newLineProvider(0)
	provider.current, provider.fixed = nil, true
	sequence, ids := planIDs(t, provider, spans, fixedClock())
	require.Equal(t, spans[0].ID, ids[0])
	require.ElementsMatch(t, logicalIDs(spans), ids)
	require.Contains(t, sequence.Reason(), "after the fixed first LOGICAL member")

	// Without a first-member forecast the origin remains unknown and LOGICAL is kept.
	provider.fixed = false
	sequence, ids = planIDs(t, provider, spans, fixedClock())
	require.Equal(t, reasonInitial, sequence.Reason())
	require.Equal(t, logicalIDs(spans), ids)
}

func TestPlanSkipsUnknownCandidateEdges(t *testing.T) {
	// Every jump into the far cluster from the near cluster is unknown except along LOGICAL itself.
	spans := []Span{lineSpan(1, 0, 1), lineSpan(2, 9000, 9001), lineSpan(3, 2, 3), lineSpan(4, 9002, 9003),
		lineSpan(5, 4, 5), lineSpan(6, 9004, 9005)}
	provider := newLineProvider(0)
	logical := map[[2]uint64]bool{{0, 0}: true}
	for i := 1; i < len(spans); i++ {
		logical[[2]uint64{spans[i-1].Ranges[0].End.LogicalObject, spans[i].Ranges[0].Start.LogicalObject}] = true
	}
	provider.unknown = func(from, start uint64) bool { return from == 3 && start == 4 && !logical[[2]uint64{from, start}] }
	sequence, ids := planIDs(t, provider, spans, fixedClock())
	require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, sequence.Order(), sequence.Reason())
	require.ElementsMatch(t, logicalIDs(spans), ids)
	require.NotEqual(t, []int64{3, 5}, ids[slices.Index(ids, 3):min(len(ids), slices.Index(ids, 3)+2)])
}

func TestPlanRecordsFallbackReasons(t *testing.T) {
	spans := shuffledSpans(20, 5)
	for _, test := range []struct {
		reason string
		edit   func(*lineProvider)
	}{
		{reasonCanonical, func(p *lineProvider) { p.canonical = false }},
		{reasonIncomplete, func(p *lineProvider) { p.indexed = false }},
		{reasonInitial, func(p *lineProvider) { p.current = nil }},
		{reasonCosts, func(p *lineProvider) { p.unknown = func(uint64, uint64) bool { return true } }},
	} {
		provider := newLineProvider(0)
		test.edit(provider)
		sequence, ids := planIDs(t, provider, spans, fixedClock())
		require.Equal(t, test.reason, sequence.Reason())
		require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL, sequence.Order())
		require.Equal(t, logicalIDs(spans), ids)
	}

	// Members without ranges and without a current Index source cannot be priced.
	missing := slices.Clone(spans)
	missing[3] = Span{ID: missing[3].ID, Path: "x"}
	sequence, ids := planIDs(t, newLineProvider(0), missing, fixedClock())
	require.Equal(t, reasonIncomplete, sequence.Reason())
	require.Equal(t, logicalIDs(spans), ids)
}

func TestPlanFillsMissingRangesFromCurrentIndex(t *testing.T) {
	spans := shuffledSpans(30, 9)
	var entries []RangeEntry
	selected := slices.Clone(spans)
	for i, span := range spans {
		path := string(rune('a' + i))
		entries = append(entries, RangeEntry{Path: path, Size: 1, Ranges: span.Ranges}, RangeEntry{Path: "unselected-" + path, Size: 1})
		if i%2 == 0 {
			selected[i] = Span{ID: span.ID, Path: path, Size: 1}
		}
	}
	provider := indexedLineProvider{newLineProvider(0)}
	provider.entries = entries
	sequence, ids := planIDs(t, provider, selected, fixedClock())
	require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_AUTO, sequence.Order(), sequence.Reason())
	require.ElementsMatch(t, logicalIDs(spans), ids)

	// A duplicate or size-mismatched path, or an unreadable Index, keeps LOGICAL without partial planning.
	for _, edit := range []func(*lineProvider){
		func(p *lineProvider) { p.entries = append(slices.Clone(entries), entries[0]) },
		func(p *lineProvider) { p.entries = slices.Clone(entries); p.entries[0].Size = 2 },
		func(p *lineProvider) { p.walkErr = errors.New("index unavailable") },
	} {
		provider := indexedLineProvider{newLineProvider(0)}
		edit(provider.lineProvider)
		sequence, ids := planIDs(t, provider, selected, fixedClock())
		require.Equal(t, entity.TapeReadOrder_TAPE_READ_ORDER_LOGICAL, sequence.Order())
		require.Contains(t, sequence.Reason(), "LOGICAL: current read ranges")
		require.Equal(t, logicalIDs(spans), ids)
	}
}
