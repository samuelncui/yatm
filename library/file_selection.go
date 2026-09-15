package library

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

// WalkSelectedFiles expands logical selections in bounded pages with full Library-relative paths.
func (l *Library) WalkSelectedFiles(ctx context.Context, ids []int64, yield func(*File, string) error) error {
	// Keep only explicit root identities; descendants need no whole-tree visited set.
	selected := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return fmt.Errorf("invalid selected File ID %d", id)
		}
		selected[id] = struct{}{}
	}
	ordered := make([]int64, 0, len(selected))
	for id := range selected {
		ordered = append(ordered, id)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })

	// Resolve complete root paths and remove roots already covered by another selected ancestor.
	for _, id := range ordered {
		file, err := l.GetFile(ctx, id)
		if err != nil {
			return err
		}
		parts := []string{file.Name}
		parentID := file.ParentID
		covered := false
		for parentID != 0 {
			if len(parts) > 256 {
				return fmt.Errorf("selected Library path exceeds 256 levels or is cyclic")
			}
			if _, ok := selected[parentID]; ok {
				covered = true
				break
			}
			parent, err := l.GetFile(ctx, parentID)
			if err != nil {
				return err
			}
			parts = append(parts, parent.Name)
			parentID = parent.ParentID
		}
		if covered {
			continue
		}
		for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
			parts[left], parts[right] = parts[right], parts[left]
		}
		if err := l.walkSelected(ctx, file, strings.Join(parts, "/"), 0, yield); err != nil {
			return err
		}
	}
	return nil
}

func (l *Library) walkSelected(ctx context.Context, file *File, target string, depth int, yield func(*File, string) error) error {
	// Validate every resulting logical target before descending or yielding content.
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 256 {
		return fmt.Errorf("selected Library tree exceeds 256 levels")
	}
	if err := entity.ValidateRelativePath(target); err != nil {
		return err
	}
	if !fs.FileMode(file.Mode).IsDir() {
		if !fs.FileMode(file.Mode).IsRegular() {
			return fmt.Errorf("selected File is not regular, file_id=%d", file.ID)
		}
		return yield(file, target)
	}

	// Retain one bounded direct-child page at each active depth.
	var after string
	for {
		rows, err := l.ListPage(ctx, file.ID, after, batchSize)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err := l.walkSelected(ctx, row, path.Join(target, row.Name), depth+1, yield); err != nil {
				return err
			}
		}
		after = rows[len(rows)-1].Name
	}
}
