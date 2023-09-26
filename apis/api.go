package apis

import (
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
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
