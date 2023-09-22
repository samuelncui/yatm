package apis

import (
	"github.com/samuelncui/tapewriter/entity"
	"github.com/samuelncui/tapewriter/executor"
	"github.com/samuelncui/tapewriter/library"
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
