package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
)

// ErrMeasureExcluded reports a measured root that Location Ignore excludes.
var ErrMeasureExcluded = errors.New("Location Ignore excludes the measured path")

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

// LocationMeasurement reads the directories of one measured Location root without crossing a
// mount or another registered Location.
type LocationMeasurement struct {
	executor   *Executor
	Location   *library.Location
	boundaries []string
}

// PrepareLocationMeasurement admits one measured directory. Location Ignore excludes this
// Location's content, so it also excludes a measured root.
func (e *Executor) PrepareLocationMeasurement(ctx context.Context, ref *entity.LocationEntryRef) (*LocationMeasurement, error) {
	location, err := e.lib.GetLocation(ctx, ref.LocationId)
	if err != nil {
		return nil, err
	}
	_, info, err := e.CheckLocationPath(location, ref.Path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, library.ErrLocationConflict
	}
	if e.LocationExcluded(location, ref.Path, true) {
		return nil, ErrMeasureExcluded
	}
	boundaries, err := e.lib.ReadLocationBoundaries(ctx, location)
	if err != nil {
		return nil, err
	}
	return &LocationMeasurement{executor: e, Location: location, boundaries: boundaries}, nil
}

// ReadDirectory reads one measured directory after refusing a nested mount, including a bind
// mount, or another registered Location.
func (m *LocationMeasurement) ReadDirectory(ctx context.Context, relative string, visit func([]*entity.LocationEntry, map[string]os.FileInfo) error) error {
	full := filepath.Join(m.Location.RootPath, filepath.FromSlash(relative))
	same, err := SameMount(m.Location.RootPath, full)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("measurement cannot cross a mount boundary: %q", relative)
	}

	// Registration boundaries remain provider-owned even when directories are explicitly selected.
	for _, root := range m.boundaries {
		if full == root || strings.HasPrefix(full, root+string(filepath.Separator)) {
			return fmt.Errorf("measurement crosses another registered Location at %q", root)
		}
	}
	return m.executor.ReadObservedLocationDirectory(ctx, m.Location, relative, visit)
}
