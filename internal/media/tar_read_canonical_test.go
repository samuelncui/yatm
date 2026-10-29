package media

import (
	"testing"
	"time"

	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/samuelncui/yatm/internal/media/tape"
	"github.com/stretchr/testify/require"
)

func TestTarReadCanonicalStateMatchesEveryEligibleMethod(t *testing.T) {
	// The same final record owns the exit after seek, continuation, and cache-only duplicate reads.
	session := &tarReadSession{options: TarSessionOptions{Mode: tape.RecordMode{MaximumBytes: 4096},
		ReadModel: tarReadModelFixture(t, nil), ReadTiming: tarReadTimingFixture()}, navigator: &tarPositionDevice{bits: 64}}
	provider := &tarReadOrderProvider{session: session}
	cases := []struct {
		name     string
		interval readplan.Range
		incoming readplan.State
		method   readplan.Method
	}{
		{"seek", tarCostRange(1, 101, 104, 0), readplan.State{Position: readOrderPosition(500), Stream: tarPrefix(1)}, readplan.MethodSeek},
		{"continue", tarCostRange(1, 101, 104, 0), readplan.State{Position: readOrderPosition(101), Stream: tarPrefix(1)}, readplan.MethodContinue},
		{"duplicate cached member", tarCostRange(1, 101, 101, 2048), readplan.State{Position: readOrderPosition(102), Cache: readOrderPosition(101), Stream: tarPrefix(1)}, readplan.MethodCache},
		{"empty member header", tarCostRange(1, 101, 101, 512), readplan.State{Position: readOrderPosition(102), Cache: readOrderPosition(101), Stream: tarPrefix(1)}, readplan.MethodCache},
		{"cached header and multirecord tail", tarCostRange(1, 101, 103, 512), readplan.State{Position: readOrderPosition(102), Cache: readOrderPosition(101), Stream: tarPrefix(1)}, readplan.MethodCache},
		{"append boundaries", tarCostRange(3, 110, 111, 512), readplan.State{Position: readOrderPosition(101), Stream: tarPrefix(1)}, readplan.MethodContinue},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			exit, known := provider.CanonicalReadState(readplan.Span{ID: 1, Ranges: []readplan.Range{test.interval}})
			require.True(t, known)
			transition, known := provider.Evaluate(test.incoming, test.interval)
			require.True(t, known)
			require.Equal(t, test.method, transition.Method)
			require.True(t, sameTarReadState(exit, transition.Next))
		})
	}
}

func TestTarReadCanonicalStateRejectsMalformedReachableCache(t *testing.T) {
	// Certification checks the real session reader before relying on the incoming-independent invariant.
	session := &tarReadSession{options: TarSessionOptions{Mode: tape.RecordMode{MaximumBytes: 4096},
		ReadModel: tarReadModelFixture(t, nil), ReadTiming: tarReadTimingFixture(), ReadPlanningBudget: time.Hour},
		navigator: &tarPositionDevice{bits: 64}, file: 1, fileKnown: true,
		records: &tarRecordReader{object: 101, nextObject: 104, known: true, nextKnown: true, length: 4096}}
	provider := &tarReadOrderProvider{session: session}
	span := readplan.Span{ID: 1, Ranges: []readplan.Range{tarCostRange(1, 101, 101, 512)}}
	_, known := provider.CanonicalReadState(span)
	require.False(t, known, "a cache-only transition may preserve the malformed cursor, so no delta certificate is possible")
	require.Equal(t, time.Hour, provider.ReadPlanningBudget())

	// A canonical real cache permits the same member, while disjoint multi-range spans remain generic.
	session.records.nextObject = 102
	_, known = provider.CanonicalReadState(span)
	require.True(t, known)
	span.Ranges = append(span.Ranges, tarCostRange(1, 104, 105, 0))
	_, known = provider.CanonicalReadState(span)
	require.False(t, known)
}
