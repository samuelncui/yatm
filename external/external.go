package external

import "github.com/abc950309/tapewriter/library"

type External struct {
	lib *library.Library
}

func New(lib *library.Library) *External {
	return &External{lib: lib}
}
