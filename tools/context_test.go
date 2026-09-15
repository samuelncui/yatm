package tools

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestWithoutTimeout(t *testing.T) {
	type contextKey struct{}
	parent := context.WithValue(context.Background(), contextKey{}, "value")
	parent, cancel := context.WithTimeout(parent, time.Hour)
	detached := WithoutTimeout(parent)
	cancel()

	_, hasDeadline := detached.Deadline()
	require.False(t, hasDeadline)
	require.Nil(t, detached.Done())
	require.NoError(t, detached.Err())
	require.Equal(t, "value", detached.Value(contextKey{}))
}
