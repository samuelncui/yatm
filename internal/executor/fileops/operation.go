package fileops

import (
	"context"

	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/resource"
)

type operation struct {
	*resource.TemporaryDB
	exe *executor.Executor
}

func newOperation(ctx context.Context, exe *executor.Executor) (*operation, error) {
	// The request owns its disposable manifest through result delivery.
	temporary, err := resource.OpenTemporaryDB(exe.Paths().Work, "file-operation-")
	if err != nil {
		return nil, err
	}
	return &operation{TemporaryDB: temporary, exe: exe}, nil
}
