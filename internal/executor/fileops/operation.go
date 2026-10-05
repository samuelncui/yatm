package fileops

import (
	"context"

	"github.com/google/uuid"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/resource"
)

type operation struct {
	*resource.TemporaryDB
	exe *executor.Executor
}

func newOperation(ctx context.Context, exe *executor.Executor) (*operation, error) {
	// The shared resource owns cleanup; the operation keeps its existing temporary namespace.
	prefix := ".yatm-fileops-" + uuid.NewString() + "-"
	temporary, err := resource.OpenTemporaryDB("", prefix)
	if err != nil {
		return nil, err
	}
	return &operation{TemporaryDB: temporary, exe: exe}, nil
}
