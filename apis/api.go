package apis

import (
	"github.com/abc950309/tapewriter/entity"
	"github.com/abc950309/tapewriter/executor"
	"github.com/abc950309/tapewriter/library"
)

var (
	_ = entity.ServiceServer(&API{})
)

type API struct {
	entity.UnsafeServiceServer

	lib        *library.Library
	exe        *executor.Executor
	sourceBase string
}

func New(base string, lib *library.Library, exe *executor.Executor) *API {
	return &API{lib: lib, exe: exe, sourceBase: base}
}
