package external

import "github.com/samuelncui/tapemanager/library"

type External struct {
	lib *library.Library
}

func New(lib *library.Library) *External {
	return &External{lib: lib}
}
