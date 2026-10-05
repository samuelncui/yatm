package treeops

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type settlementStore struct {
	*objectStore
	t      *testing.T
	cancel context.CancelFunc
}

func (s *settlementStore) ApplyPrimitive(ctx context.Context, p Primitive) (Receipt, error) {
	// Cancel the request at the physical side-effect boundary, after this primitive was admitted.
	s.cancel()
	require.NoError(s.t, ctx.Err())
	require.Nil(s.t, context.Cause(ctx))
	_, deadline := ctx.Deadline()
	require.False(s.t, deadline)
	return s.objectStore.ApplyPrimitive(ctx, p)
}

func TestPhysicalReceiptPersistsAfterCancellation(t *testing.T) {
	// A two-file operation must record its first completed primitive before stopping the second.
	s := &objectStore{nodes: make(map[string]Node)}
	s.add("", true)
	s.add("a", false)
	s.add("b", false)
	s.add("to", true)
	e := objectEngine(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.store = &settlementStore{objectStore: s, t: t, cancel: cancel}
	require.NoError(t, e.Prepare(ctx, Request{Kind: Move, Sources: []string{"object:a", "object:b"}, Destination: "prefix:to"}))
	var results []Result
	err := e.Run(ctx, func(result Result) error { results = append(results, result); return nil })

	// The disposable receipt and emitted outcome agree even though the request can no longer query.
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, results, 1)
	require.Equal(t, Succeeded, results[0].Outcome)
	var stored step
	require.NoError(t, e.db.First(&stored, results[0].ID).Error)
	require.Equal(t, Succeeded, stored.Outcome)
	require.Contains(t, s.nodes, "object:to/a")
	require.Contains(t, s.nodes, "object:b")
	require.NotContains(t, s.nodes, "object:to/b")
}
