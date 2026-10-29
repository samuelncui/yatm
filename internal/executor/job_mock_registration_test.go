package executor

import (
	"context"
	"errors"

	"github.com/samuelncui/yatm/entity"
	"google.golang.org/grpc"
)

func init() {
	// Ordinary catalog/lifecycle tests use mock runners without registering the production Job packages.
	register := func(grpc.ServiceRegistrar, *Executor) {}
	RegisterJobType(entity.JobKind_JOB_KIND_ARCHIVE, func(_ context.Context, exe *Executor, job *Job) (Runner, error) {
		return &testRunner{exe: exe, job: job}, nil
	}, register)
	RegisterJobType(entity.JobKind_JOB_KIND_RESTORE, func(_ context.Context, exe *Executor, job *Job) (Runner, error) {
		return &testRunner{exe: exe, job: job, indexErr: errors.New("injected index failure")}, nil
	}, register)
	RegisterJobType(entity.JobKind_JOB_KIND_SCAN, func(_ context.Context, exe *Executor, job *Job) (Runner, error) {
		return &testRunner{exe: exe, job: job}, nil
	}, register)
}
