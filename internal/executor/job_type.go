package executor

import (
	"context"
	"fmt"
	"sync"

	"github.com/samuelncui/yatm/entity"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

// Runner owns the common lifecycle of one typed Job implementation.
type Runner interface {
	Index(ctx context.Context) error
	Phase() entity.JobPhase
	Close() error
	Logger() *logrus.Logger
}

// RunnerFactory opens the typed runner for an existing Job Bundle.
type RunnerFactory func(ctx context.Context, exe *Executor, job *Job) (Runner, error)

// JobServiceRegistrar registers the typed public service for a Job implementation.
type JobServiceRegistrar func(server grpc.ServiceRegistrar, exe *Executor)

type jobType struct {
	newRunner       RunnerFactory
	registerService JobServiceRegistrar
}

var jobTypes = struct {
	sync.RWMutex
	values map[entity.JobKind]jobType
}{values: make(map[entity.JobKind]jobType, 2)}

// RegisterJobType pairs one Job kind with its runner and public gRPC service.
func RegisterJobType(kind entity.JobKind, factory RunnerFactory, registerService JobServiceRegistrar) {
	jobTypes.Lock()
	defer jobTypes.Unlock()

	// Reject incomplete or duplicate registrations during package initialization.
	if factory == nil {
		panic(fmt.Sprintf("register job type with nil factory, kind=%s", kind))
	}
	if registerService == nil {
		panic(fmt.Sprintf("register job type with nil service, kind=%s", kind))
	}
	if _, exists := jobTypes.values[kind]; exists {
		panic(fmt.Sprintf("register duplicate job type, kind=%s", kind))
	}

	// Keep the runner and its public service as one Job type registration.
	jobTypes.values[kind] = jobType{newRunner: factory, registerService: registerService}
}

func getRunnerFactory(kind entity.JobKind) (RunnerFactory, error) {
	jobTypes.RLock()
	factory := jobTypes.values[kind].newRunner
	jobTypes.RUnlock()

	if factory == nil {
		return nil, fmt.Errorf("job runner is not registered, kind=%s", kind)
	}
	return factory, nil
}

// RegisterJobServices registers the gRPC service paired with every Job runner type.
func (e *Executor) RegisterJobServices(server grpc.ServiceRegistrar) {
	// Snapshot registrations without invoking external code under the registry lock.
	jobTypes.RLock()
	registrars := make([]JobServiceRegistrar, 0, len(jobTypes.values))
	for _, registered := range jobTypes.values {
		registrars = append(registrars, registered.registerService)
	}
	jobTypes.RUnlock()

	// Register every typed service before the gRPC server starts serving.
	for _, register := range registrars {
		register(server, e)
	}
}
