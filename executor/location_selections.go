package executor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
)

// WalkLiveSelections expands only selected physical scopes and admits their ordinary files.
// Job manifests retain responsibility for deduplicating File IDs across mixed logical/physical roots.
func (e *Executor) WalkLiveSelections(ctx context.Context, db *gorm.DB, selections []*entity.FileSelection, yield func(*library.File, string) error) error {
	// User roots are bounded; no in-memory visited set grows with the number of descendants.
	if len(selections) == 0 || len(selections) > 1000 {
		return fmt.Errorf("select between 1 and 1000 roots")
	}
	var logical []*entity.FileSelection
	physical := map[int64][]*entity.LocationSelection{}
	for _, selection := range selections {
		if selection == nil {
			return fmt.Errorf("missing selection")
		}
		if selection.GetLibrary() != nil {
			logical = append(logical, selection)
		}
		if target := selection.GetLocation(); target != nil {
			physical[target.LocationId] = append(physical[target.LocationId], target)
		}
	}
	locations := make([]int64, 0, len(physical))
	for id := range physical {
		locations = append(locations, id)
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i] < locations[j] })
	for _, id := range locations {
		if err := e.stageLiveSelections(ctx, db, id, physical[id]); err != nil {
			return err
		}
		if err := e.yieldStagedSelections(ctx, db, id, yield); err != nil {
			return err
		}
	}
	if len(logical) == 0 {
		return nil
	}
	return e.lib.WalkFileSelections(ctx, logical, yield)
}

func (e *Executor) yieldSelectedFile(ctx context.Context, fileID int64, yield func(*library.File, string) error) error {
	file, err := e.lib.GetFile(ctx, fileID)
	if err != nil {
		return err
	}
	parents, err := e.lib.ListParents(ctx, fileID)
	if err != nil {
		return err
	}
	parts := make([]string, 0, len(parents))
	for _, parent := range parents {
		parts = append(parts, parent.Name)
	}
	if err := e.lib.HydrateFileContent(ctx, file); err != nil {
		return err
	}
	return yield(file, strings.Join(parts, "/"))
}

func (e *Executor) walkLiveSelection(ctx context.Context, location *library.Location, ref *entity.LocationEntryRef, recursive bool, depth int, yield func(*entity.LocationEntryRef) error) error {
	return e.walkLiveSelectionScope(ctx, location, ref, recursive, depth, yield, nil)
}

func (e *Executor) walkLiveSelectionScope(ctx context.Context, location *library.Location, ref *entity.LocationEntryRef, recursive bool, depth int, yield, directory func(*entity.LocationEntryRef) error) error {
	// Directory selection applies Ignore; a directly selected ordinary file remains explicit.
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 256 {
		return fmt.Errorf("selected directory exceeds depth limit")
	}
	_, full, info, err := e.ResolveLocationEntry(ctx, ref)
	if err != nil {
		return err
	}
	if recursive && location.Excluded(ref.Path, info.IsDir()) {
		return nil
	}
	if info.Mode().IsRegular() {
		return yield(ref)
	}
	if !info.IsDir() {
		return nil
	}
	if location.Excluded(ref.Path, true) {
		return nil
	}
	if directory != nil {
		if err := directory(ref); err != nil {
			return err
		}
	}
	dir, err := os.Open(full)
	if err != nil {
		return err
	}
	defer dir.Close()

	// Fixed-size directory reads preserve cancellation and bounded memory for deep trees.
	for {
		entries, readErr := dir.ReadDir(256)
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		for _, entry := range entries {
			relative := path.Join(ref.Path, entry.Name())
			if location.Excluded(relative, entry.IsDir()) {
				continue
			}
			observed, err := e.ObserveLocationEntry(ctx, location.ID, relative)
			if err != nil {
				return err
			}
			if err := e.walkLiveSelectionScope(ctx, location, observed.Reference, true, depth+1, yield, directory); err != nil {
				return err
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	_, _, _, err = e.ResolveLocationEntry(ctx, ref)
	return err
}
