package executor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor/observation"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/resource"
	"gorm.io/gorm"
)

// WalkLiveSelections expands only selected physical scopes and admits their ordinary files.
// Job manifests retain responsibility for deduplicating File IDs across mixed logical/physical roots.
func (e *Executor) WalkLiveSelections(ctx context.Context, db *gorm.DB, selections []*entity.FileSelection, yield func(*library.File, string) error) (rerr error) {
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

	// Logical-only selections do not require a physical preparation database.
	if len(locations) == 0 {
		return e.lib.WalkOriginalSelections(ctx, logical, yield)
	}

	// Preparation owns one disposable database and closes it before removing its files.
	if err := os.MkdirAll(e.paths.Work, 0700); err != nil {
		return err
	}
	temporary, err := resource.OpenTemporaryDB(e.paths.Work, ".selection-")
	if err != nil {
		return err
	}
	defer func() { rerr = errors.Join(rerr, temporary.Close()) }()
	staged := temporary.DB
	if err := staged.AutoMigrate(&observation.Item{}); err != nil {
		return err
	}
	for _, id := range locations {
		if err := e.stageLiveSelections(ctx, staged, id, physical[id]); err != nil {
			return err
		}
		if err := e.yieldStagedSelections(ctx, staged, yield); err != nil {
			return err
		}
	}
	if len(logical) == 0 {
		return nil
	}
	return e.lib.WalkOriginalSelections(ctx, logical, yield)
}

func (e *Executor) walkLiveSelection(ctx context.Context, location *library.Location, ref *entity.LocationEntryRef, recursive bool, depth int, yield func(*entity.LocationEntryRef) error) error {
	// Resolve the explicit root once; descendants reuse the directory reader's metadata.
	_, info, err := e.checkPreparedLocationPath(location, location.RootPath, ref.Path)
	if err != nil {
		return err
	}
	entry, err := locationEntry(location.ID, ref.Path, info)
	if err != nil {
		return err
	}
	return e.walkLocationSelection(ctx, location, entry.Reference, info, recursive, depth, nil,
		func(ref *entity.LocationEntryRef, _ os.FileInfo) error { return yield(ref) }, nil)
}

func (e *Executor) walkLocationSelection(ctx context.Context, location *library.Location, ref *entity.LocationEntryRef, info os.FileInfo, recursive bool, depth int, covered []string,
	yield func(*entity.LocationEntryRef, os.FileInfo) error, directory func(*entity.LocationEntryRef) error) error {
	// Explicit roots remain checked; enumerated descendants already passed the same scoped rules.
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 256 {
		return fmt.Errorf("selected directory exceeds depth limit")
	}
	if IsLocationTrashPath(ref.Path) {
		if recursive {
			return nil
		}
		return fmt.Errorf("Trash content cannot be selected for a Job")
	}
	if !recursive && location.Excluded(ref.Path, info.IsDir()) {
		return nil
	}
	// Earlier selected roots own their complete subtrees, including empty directories.
	for _, name := range covered {
		if name == "" || ref.Path == name || strings.HasPrefix(ref.Path, name+"/") {
			return nil
		}
	}
	if info.Mode().IsRegular() {
		return yield(ref, info)
	}
	if !info.IsDir() {
		return nil
	}
	if directory != nil {
		if err := directory(ref); err != nil {
			return err
		}
	}

	// Each directory consumes one sequential stream and each leaf's metadata reaches its consumer once.
	return e.ReadObservedLocationDirectory(ctx, location, ref.Path, func(rows []*entity.LocationEntry, infos map[string]os.FileInfo) error {
		for _, row := range rows {
			if err := e.walkLocationSelection(ctx, location, row.Reference, infos[row.Path], true, depth+1, covered, yield, directory); err != nil {
				return err
			}
		}
		return nil
	})
}
