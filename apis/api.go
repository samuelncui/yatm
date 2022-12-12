package apis

import (
	"github.com/abc950309/tapewriter/entity"
	"github.com/abc950309/tapewriter/executor"
	"github.com/abc950309/tapewriter/library"
)

// JobGet(context.Context, *entity.JobGetRequest) (*entity.JobGetReply, error)

type API struct {
	entity.UnimplementedServiceServer

	lib        *library.Library
	exe        *executor.Executor
	sourceBase string
}

func New(base string, lib *library.Library, exe *executor.Executor) *API {
	return &API{lib: lib, exe: exe, sourceBase: base}
}
