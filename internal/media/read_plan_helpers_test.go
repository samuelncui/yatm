package media

import (
	"math"
	"os"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"iter"
	"testing"

	"github.com/samuelncui/yatm/internal/media/readplan"
	"github.com/stretchr/testify/require"
)

func sequenceTestInput(spans []readplan.Span) iter.Seq2[readplan.Span, error] {
	return func(yield func(readplan.Span, error) bool) {
		for _, span := range spans {
			if !yield(span, nil) {
				return
			}
		}
	}
}

func sequenceTestIDs(t *testing.T, sequence *readplan.Sequence, batch int) []int64 {
	t.Helper()
	var ids []int64
	for {
		page, err := sequence.Next(t.Context(), batch)
		require.NoError(t, err)
		if len(page) == 0 {
			return ids
		}
		ids = append(ids, page...)
	}
}

func readModelFloat(value float64) *float64 { return &value }

type readScoreProvider interface {
	AccessScore(from, to *entity.TapeBytePosition) (float64, bool)
}

func usableReadScore(provider ReadOrderProvider, from, to *entity.TapeBytePosition) (float64, bool) {
	scorer, ok := provider.(readScoreProvider)
	if !ok {
		return 0, false
	}
	score, known := scorer.AccessScore(from, to)
	return score, known && !math.IsNaN(score) && !math.IsInf(score, 0)
}

func requireSequenceRootEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(resource.TemporaryRoot(root))
	if os.IsNotExist(err) {
		return
	}
	require.NoError(t, err)
	require.Empty(t, entries, "the disposable execution index must not survive cleanup")
}
