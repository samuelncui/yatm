package apis

import (
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
)

var (
	_ entity.ServiceServer    = (*API)(nil)
	_ entity.JobServiceServer = (*API)(nil)
)

type API struct {
	entity.UnsafeServiceServer
	entity.UnimplementedJobServiceServer

	lib *library.Library
	exe *executor.Executor
}

func New(lib *library.Library, exe *executor.Executor) *API {
	return &API{lib: lib, exe: exe}
}
