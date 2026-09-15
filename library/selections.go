package library

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/samuelncui/yatm/entity"
)

// FreezeSelections resolves presentation defaults and captures physical-index revisions before Create returns.
func (l *Library) FreezeSelections(ctx context.Context, selections []*entity.FileSelection) error {
	// Bound explicit user input; descendants are streamed later by the Job runner.
	if len(selections) == 0 || len(selections) > 1000 {
		return fmt.Errorf("select between 1 and 1000 files or directories")
	}
	for _, selection := range selections {
		if selection == nil {
			return fmt.Errorf("File selection is missing")
		}
		switch target := selection.Target.(type) {
		case *entity.FileSelection_Library:
			if target.Library == nil || target.Library.FileId < 0 {
				return fmt.Errorf("invalid Library selection")
			}
			scope, err := l.ResolveFileScope(ctx, selection.Scope)
			if err != nil {
				return err
			}
			selection.Scope = scope
		case *entity.FileSelection_Location:
			if target.Location == nil {
				return fmt.Errorf("Location selection is missing")
			}
			location, err := l.GetOnlineSource(ctx, target.Location.LocationId)
			if err != nil {
				return err
			}
			if target.Location.Path != "" {
				target.Location.Path = strings.TrimSuffix(target.Location.Path, "/")
				if err := entity.ValidateRelativePath(target.Location.Path); err != nil {
					return err
				}
			}
			target.Location.Revision = location.Revision
			if ref := target.Location.Reference; ref != nil {
				if ref.LocationId != location.ID || ref.Path != target.Location.Path || ref.BindingToken != location.BindingToken {
					return ErrOnlineConflict
				}
			} else {
				target.Location.Reference = &entity.LocationEntryRef{LocationId: location.ID, Path: target.Location.Path, BindingToken: location.BindingToken}
			}
			selection.Scope = entity.FileScope_FILE_SCOPE_ALL
		default:
			return fmt.Errorf("selection requires Library or Location")
		}
	}
	return nil
}

type selectionRoot struct {
	file       *File
	path       string
	locationID int64
	revision   int64
	scope      entity.FileScope
}

func underSelection(value, root string) bool {
	return root == "" || value == root || strings.HasPrefix(value, root+"/")
}

// WalkFileSelections expands mixed physical/logical selections without a whole-manifest visited set.
func (l *Library) WalkFileSelections(ctx context.Context, selections []*entity.FileSelection, yield func(*File, string) error) error {
	// Resolve only the explicitly selected roots, retaining the Create-time visibility scope.
	roots := make([]selectionRoot, 0, len(selections))
	for _, selection := range selections {
		if selection == nil {
			return fmt.Errorf("File selection is missing")
		}
		if target := selection.GetLocation(); target != nil {
			location, err := l.GetOnlineSource(ctx, target.LocationId)
			if err != nil {
				return err
			}
			if location.Revision != target.Revision {
				return ErrOnlineConflict
			}
			roots = append(roots, selectionRoot{path: target.Path, locationID: target.LocationId, revision: target.Revision, scope: entity.FileScope_FILE_SCOPE_ALL})
			continue
		}
		target := selection.GetLibrary()
		if target == nil {
			return fmt.Errorf("Library selection is missing")
		}
		root := selectionRoot{scope: selection.Scope, file: &File{Kind: entity.FileKind_FILE_KIND_DIRECTORY}}
		if target.FileId != 0 {
			file, err := l.GetFile(ctx, target.FileId)
			if err != nil {
				return err
			}
			paths, err := l.resolveFilePaths(ctx, []*File{file})
			if err != nil {
				return err
			}
			root.file, root.path = file, strings.TrimPrefix(paths[file.ID], "/")
		}
		roots = append(roots, root)
	}

	// The first eligible explicit root owns each File; overlaps do not multiply work or memory.
	for index, root := range roots {
		emit := func(file *File, target string) error {
			for _, earlier := range roots[:index] {
				if earlier.locationID == 0 {
					if !underSelection(target, earlier.path) {
						continue
					}
					if earlier.scope == entity.FileScope_FILE_SCOPE_SAVED {
						var count int64
						if err := l.db.WithContext(ctx).Model(&FileVersion{}).Where("file_id = ?", file.ID).Limit(1).Count(&count).Error; err != nil {
							return err
						}
						if count == 0 {
							continue
						}
					}
					return nil
				}
				var originals []FileLocation
				if err := l.db.WithContext(ctx).Where("file_id = ? AND location_id = ?", file.ID, earlier.locationID).Limit(1).Find(&originals).Error; err != nil {
					return err
				}
				if len(originals) == 1 && underSelection(originals[0].Path, earlier.path) {
					return nil
				}
			}
			return yield(file, target)
		}
		if root.locationID == 0 {
			if err := l.walkSelectionTree(ctx, root.file, root.path, root.scope, 0, emit); err != nil {
				return err
			}
			continue
		}
		if err := l.walkPhysicalSelection(ctx, root, emit); err != nil {
			return err
		}
	}
	return nil
}

func (l *Library) walkSelectionTree(ctx context.Context, file *File, target string, scope entity.FileScope, depth int, yield func(*File, string) error) error {
	// Exclude unversioned leaves before yielding; logical directories remain traversable.
	if depth > maxFilePathDepth {
		return fmt.Errorf("Library selection exceeds maximum depth")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if file.Kind != entity.FileKind_FILE_KIND_DIRECTORY {
		if scope == entity.FileScope_FILE_SCOPE_SAVED {
			var count int64
			if err := l.db.WithContext(ctx).Model(&FileVersion{}).Where("file_id = ?", file.ID).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				return nil
			}
		}
		if err := entity.ValidateRelativePath(target); err != nil {
			return err
		}
		return yield(file, target)
	}

	// Keep one bounded child page per active depth, with visibility applied in the database.
	cursor := ""
	for {
		page, err := l.ListFiles(ctx, file.ID, scope, cursor, batchSize)
		if err != nil {
			return err
		}
		for _, child := range page.Files {
			if err := l.walkSelectionTree(ctx, child, path.Join(target, child.Name), scope, depth+1, yield); err != nil {
				return err
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

func (l *Library) walkPhysicalSelection(ctx context.Context, root selectionRoot, yield func(*File, string) error) error {
	// The published path index supplies descendants; no live filesystem traversal participates.
	after := ""
	found := false
	for {
		query := l.db.WithContext(ctx).Where("location_id = ? AND path > ?", root.locationID, after)
		if root.path != "" {
			// Binary path ranges preserve filesystem case and use the Location/path index.
			query = query.Where("path = ? OR (path >= ? AND path < ?)", root.path, root.path+"/", root.path+"0")
		}
		var originals []*FileLocation
		if err := query.Order("path").Limit(batchSize).Find(&originals).Error; err != nil {
			return err
		}
		if len(originals) == 0 {
			break
		}
		ids := make([]int64, 0, len(originals))
		for _, original := range originals {
			ids = append(ids, original.FileID)
		}
		byID, err := l.mGetFile(ctx, l.db.WithContext(ctx), ids...)
		if err != nil {
			return err
		}
		files := make([]*File, 0, len(byID))
		for _, original := range originals {
			if file := byID[original.FileID]; file != nil {
				files = append(files, file)
			}
		}
		paths, err := l.resolveFilePaths(ctx, files)
		if err != nil {
			return err
		}
		for _, original := range originals {
			file := byID[original.FileID]
			if file == nil {
				return fmt.Errorf("Location references missing File %d", original.FileID)
			}
			if err := yield(file, strings.TrimPrefix(paths[file.ID], "/")); err != nil {
				return err
			}
			found = true
		}
		after = originals[len(originals)-1].Path
	}

	// Never finish a manifest assembled across two different published physical indexes.
	location, err := l.GetOnlineSource(ctx, root.locationID)
	if err != nil {
		return err
	}
	if location.Revision != root.revision {
		return ErrOnlineConflict
	}
	if !found && root.path != "" {
		return fmt.Errorf("selected Location path has no indexed files: %q", root.path)
	}
	return nil
}
