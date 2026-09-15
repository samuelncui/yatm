package fileops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
)

func (r *operation) checkDestination(location *library.Location, destination *entity.LocationEntryRef) error {
	if destination == nil {
		return nil
	}
	_, info, err := r.exe.CheckLocationPath(location, destination.Path)
	if err != nil {
		return err
	}
	if !factsMatch(destination.Facts, info, true) {
		return fmt.Errorf("operation destination directory was replaced")
	}
	return nil
}

func (r *operation) mutate(ctx context.Context, location *library.Location, kind entity.FileOperationKind, item *Item) error {
	// Every actual source is revalidated independently of any historical Library observation.
	source := filepath.Join(location.RootPath, filepath.FromSlash(item.SourcePath))
	target := filepath.Join(location.RootPath, filepath.FromSlash(item.TargetPath))
	if kind != entity.FileOperationKind_MAKE_DIRECTORY {
		_, info, err := r.exe.CheckLocationPath(location, item.SourcePath)
		if err != nil {
			return err
		}
		if !factsMatch(item.Facts, info, kind == entity.FileOperationKind_DELETE) {
			return fmt.Errorf("source changed: %q", item.SourcePath)
		}
		if err := r.protected(ctx, location, item.SourcePath, info); err != nil {
			return err
		}
	}
	if kind != entity.FileOperationKind_DELETE {
		if err := r.checkTarget(ctx, location, item.TargetPath); err != nil {
			return err
		}
	}

	// Native no-replace rename and individual unlink prevent overwrites and unplanned recursive deletion.
	if err := ctx.Err(); err != nil {
		return err
	}
	switch kind {
	case entity.FileOperationKind_MOVE:
		if err := renameNoReplace(source, target); err != nil {
			return fmt.Errorf("move %q failed, %w", item.SourcePath, err)
		}
	case entity.FileOperationKind_DELETE:
		if err := os.Remove(source); err != nil {
			return err
		}
		item.PhysicalDone = true
		return nil
	case entity.FileOperationKind_MAKE_DIRECTORY:
		if err := os.Mkdir(target, 0755); err != nil {
			return err
		}

	default:
		return fmt.Errorf("unsupported physical operation")
	}
	item.PhysicalDone = true
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if item.ResultFacts != nil && !factsMatch(item.ResultFacts, info, true) {
		return fmt.Errorf("operation output was replaced: %q", item.TargetPath)
	}
	item.ResultFacts = executor.LocationFacts(info)
	return nil
}
