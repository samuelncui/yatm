package executor

import (
	"context"
	"os"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
)

// ReadObservedLocationDirectory shares enumeration and metadata with measurement and selection.
func (e *Executor) ReadObservedLocationDirectory(ctx context.Context, location *library.Location, relative string, visit func([]*entity.LocationEntry, map[string]os.FileInfo) error) error {
	reader, err := e.PrepareLocationDirectory(location, relative)
	if err != nil {
		return err
	}

	// Decide each child once, then let the consumer reuse this batch's observed facts.
	return reader.ReadEntries(ctx, func(children []os.DirEntry) error {
		visible := children[:0]
		for _, child := range children {
			if !reader.Excluded(child) {
				visible = append(visible, child)
			}
		}
		rows, infos, err := reader.Observe(ctx, visible)
		if err != nil {
			return err
		}
		return visit(rows, infos)
	})
}
