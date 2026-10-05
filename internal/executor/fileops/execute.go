package fileops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
)

func (r *operation) mutate(ctx context.Context, location *library.Location, kind entity.FileOperationKind, item *Item) error {
	// Apply the path authorized and prepared by the operation planner.
	source := filepath.Join(location.RootPath, filepath.FromSlash(item.SourcePath))
	target := filepath.Join(location.RootPath, filepath.FromSlash(item.TargetPath))

	// User removal is a no-replace move; only merge cleanup may unlink a confirmed empty directory.
	if err := ctx.Err(); err != nil {
		return err
	}
	switch kind {
	case entity.FileOperationKind_FILE_OPERATION_KIND_MOVE, entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE:
		if item.UnlinkEmpty {
			if !item.Directory {
				return fmt.Errorf("only empty directories may be unlinked")
			}
			if err := os.Remove(source); err != nil {
				return err
			}
			item.PhysicalDone = true
			return nil
		}
		if err := renameNoReplace(source, target); err != nil {
			return fmt.Errorf("move %q failed, %w", item.SourcePath, err)
		}
	case entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR:
		if err := os.Mkdir(target, 0755); err != nil {
			return err
		}

	default:
		return fmt.Errorf("unsupported physical operation")
	}
	item.PhysicalDone = true
	return nil
}
