package apis

import (
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
)

var (
	_ entity.JobServiceServer = (*API)(nil)
)

type API struct {
	entity.UnimplementedJobServiceServer

	lib              *library.Library
	exe              *executor.Executor
	identicalResults identicalResultManager
}

func New(lib *library.Library, exe *executor.Executor) *API {
	return &API{lib: lib, exe: exe, identicalResults: identicalResultManager{sessions: make(map[string]*identicalResult)}}
}
