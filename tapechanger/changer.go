package tapechanger

import (
	"context"

	"github.com/abc950309/tapewriter/library"
)

var (
	tapeChangers map[string]func(dsn string) (TapeChanger, error)
)

type Tape struct {
	*library.Tape
	MountPoint string
}

type TapeChanger interface {
	Change(ctx context.Context, target *library.Tape) (*Tape, error)
}

func RegisterTapeChanger(schema string, factory func(dsn string) (TapeChanger, error)) {
	tapeChangers[schema] = factory
}
