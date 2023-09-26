package external

import "github.com/samuelncui/yatm/library"

type External struct {
	lib *library.Library
}

func New(lib *library.Library) *External {
	return &External{lib: lib}
}
