package apis

import (
	"github.com/samuelncui/tapemanager/entity"
	"github.com/samuelncui/tapemanager/executor"
	"github.com/samuelncui/tapemanager/library"
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
