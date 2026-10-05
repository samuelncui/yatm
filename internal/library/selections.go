package library

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// FreezeSelections resolves presentation defaults before Create returns.
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
			if _, err := l.GetLocation(ctx, target.Location.LocationId); err != nil {
				return err
			}
			if target.Location.Path != "" {
				target.Location.Path = strings.TrimSuffix(target.Location.Path, "/")
				if err := entity.ValidateRelativePath(target.Location.Path); err != nil {
					return err
				}
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
	scope      entity.FileScope
}

func underSelection(value, root string) bool {
	return root == "" || value == root || strings.HasPrefix(value, root+"/")
}

// WalkFileSelections expands mixed physical/logical selections without a whole-manifest visited set.
func (l *Library) WalkFileSelections(ctx context.Context, selections []*entity.FileSelection, yield func(*File, string) error) error {
	return l.walkFileSelections(ctx, selections, false, func(files []*File, targets []string) error {
		// Restore and size consumers retain compatibility facts, only for accepted leaves.
		if err := hydrateFileViews(l.readDB().WithContext(ctx), files...); err != nil {
			return err
		}
		return selectionBatchYield(yield)(files, targets)
	})
}

// ValidateOriginalSelections rejects explicit logical Trash roots before creating work.
func (l *Library) ValidateOriginalSelections(ctx context.Context, selections []*entity.FileSelection) error {
	_, _, err := l.readSelectionRoots(ctx, selections, true)
	return err
}

// WalkOriginalSelections omits Trash from recursive original-content workflows.
// Restore continues to use saved versions through the unrestricted metadata walker.
func (l *Library) WalkOriginalSelections(ctx context.Context, selections []*entity.FileSelection, yield func(*File, string) error) error {
	return l.WalkOriginalSelectionBatches(ctx, selections, selectionBatchYield(yield))
}

// WalkOriginalSelectionBatches shares ordered expansion and overlap decisions with the row walker.
func (l *Library) WalkOriginalSelectionBatches(ctx context.Context, selections []*entity.FileSelection, yield func([]*File, []string) error) error {
	return l.walkFileSelections(ctx, selections, true, yield)
}

func selectionBatchYield(yield func(*File, string) error) func([]*File, []string) error {
	return func(files []*File, targets []string) error {
		for i, file := range files {
			if err := yield(file, targets[i]); err != nil {
				return err
			}
		}
		return nil
	}
}

// readSelectionRoots owns batch identity, ancestry and explicit Trash validation.
func (l *Library) readSelectionRoots(ctx context.Context, selections []*entity.FileSelection, excludeTrash bool) (map[int64]*File, map[int64]string, error) {
	var ids []int64
	for _, selection := range selections {
		if target := selection.GetLibrary(); target != nil && target.FileId != 0 {
			ids = append(ids, target.FileId)
		}
	}
	byID, err := l.ReadFileRows(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	files := make([]*File, 0, len(byID))
	for _, id := range uniqueFileIDs(ids) {
		file := byID[id]
		if file == nil {
			return nil, nil, ErrFileNotFound
		}
		files = append(files, file)
	}
	states, err := l.resolveFilePathStates(ctx, files)
	if err != nil {
		return nil, nil, err
	}
	if excludeTrash {
		for _, state := range states {
			if _, trash := state.seen[TrashFileID]; trash {
				return nil, nil, fmt.Errorf("Library Trash cannot be scanned or backed up")
			}
		}
	}
	return byID, buildResolvedFilePaths(states), nil
}

func (l *Library) walkFileSelections(ctx context.Context, selections []*entity.FileSelection, excludeTrash bool, yield func([]*File, []string) error) error {
	// Resolve all explicit roots once, preserving their input order and effective visibility.
	byID, paths, err := l.readSelectionRoots(ctx, selections, excludeTrash)
	if err != nil {
		return err
	}
	roots := make([]selectionRoot, 0, len(selections))
	for _, selection := range selections {
		if selection == nil {
			return fmt.Errorf("File selection is missing")
		}
		if target := selection.GetLocation(); target != nil {
			roots = append(roots, selectionRoot{path: target.Path, locationID: target.LocationId, scope: entity.FileScope_FILE_SCOPE_ALL})
			continue
		}
		target := selection.GetLibrary()
		if target == nil {
			return fmt.Errorf("Library selection is missing")
		}
		scope, err := l.ResolveFileScope(ctx, selection.Scope)
		if err != nil {
			return err
		}
		root := selectionRoot{scope: scope, file: &File{Kind: entity.FileKind_FILE_KIND_DIRECTORY}}
		if target.FileId != 0 {
			root.file, root.path = byID[target.FileId], strings.TrimPrefix(paths[target.FileId], "/")
		}
		roots = append(roots, root)
	}

	// Duplicate explicit roots add no ownership information and must not multiply overlap checks.
	type rootKey struct {
		locationID int64
		path       string
		scope      entity.FileScope
	}
	seen := make(map[rootKey]bool, len(roots))
	unique := roots[:0]
	for _, root := range roots {
		key := rootKey{root.locationID, root.path, root.scope}
		if !seen[key] {
			seen[key] = true
			unique = append(unique, root)
		}
	}
	roots = unique

	// Explicit SAVED leaves need the same eligibility check as child pages, batched across roots.
	var savedIDs []int64
	for _, root := range roots {
		if root.file != nil && root.file.Kind != entity.FileKind_FILE_KIND_DIRECTORY && root.scope == entity.FileScope_FILE_SCOPE_SAVED {
			savedIDs = append(savedIDs, root.file.ID)
		}
	}
	savedRoots, err := l.savedSelectionFiles(ctx, savedIDs)
	if err != nil {
		return err
	}

	query, err := l.CompileFilesQuery("")
	if err != nil {
		return err
	}

	// Carry accepted leaves across root boundaries so explicit leaf selections also batch downstream facts.
	var accepted []*File
	var acceptedPaths []string
	deliver := func(files []*File, targets []string) error {
		for i, file := range files {
			accepted, acceptedPaths = append(accepted, file), append(acceptedPaths, targets[i])
			if len(accepted) == batchSize {
				if err := yield(accepted, acceptedPaths); err != nil {
					return err
				}
				accepted, acceptedPaths = accepted[:0], acceptedPaths[:0]
			}
		}
		return nil
	}

	// The first eligible explicit root owns each File; overlaps do not multiply work or memory.
	for index, root := range roots {
		if root.file != nil && root.file.Kind != entity.FileKind_FILE_KIND_DIRECTORY && root.scope == entity.FileScope_FILE_SCOPE_SAVED && !savedRoots[root.file.ID] {
			continue
		}
		// Overlap decisions consume the same bounded facts for all leaves in this batch.
		var pending []*File
		var targets []string
		flush := func() error {
			if len(pending) == 0 {
				return nil
			}
			keep, err := l.selectionRemainder(ctx, pending, targets, roots[:index])
			if err != nil {
				return err
			}
			files, paths := pending[:0], targets[:0]
			for i, file := range pending {
				if keep[i] {
					files, paths = append(files, file), append(paths, targets[i])
				}
			}
			if len(files) > 0 {
				if err := deliver(files, paths); err != nil {
					return err
				}
			}
			pending, targets = pending[:0], targets[:0]
			return nil
		}
		emit := func(file *File, target string) error {
			pending, targets = append(pending, file), append(targets, target)
			if len(pending) == batchSize {
				return flush()
			}
			return nil
		}
		if root.locationID == 0 {
			if err := l.walkSelectionTree(ctx, root.file, root.path, root.scope, 0, excludeTrash, roots[:index], query, emit); err != nil {
				return err
			}
		} else if err := l.walkPhysicalSelection(ctx, root, roots[:index], emit); err != nil {
			return err
		}
		if err := flush(); err != nil {
			return err
		}
	}
	if len(accepted) > 0 {
		return yield(accepted, acceptedPaths)
	}
	return nil
}

// selectionRemainder preserves first-root ownership without a query per candidate/root pair.
func (l *Library) selectionRemainder(ctx context.Context, files []*File, targets []string, earlier []selectionRoot) ([]bool, error) {
	// Fetch only the facts needed by preceding physical or SAVED roots.
	ids := make([]int64, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.ID)
	}
	physical, saved := false, false
	for _, root := range earlier {
		physical = physical || root.locationID != 0
		if root.locationID == 0 && root.scope == entity.FileScope_FILE_SCOPE_SAVED {
			for _, target := range targets {
				if underSelection(target, root.path) {
					saved = true
					break
				}
			}
		}
	}
	var originals map[int64]*FileReadFacts
	if physical {
		var err error
		originals, err = l.ReadFileFacts(ctx, ids, false, false)
		if err != nil {
			return nil, err
		}
	}
	var versions map[int64]bool
	if saved {
		var err error
		versions, err = l.savedSelectionFiles(ctx, ids)
		if err != nil {
			return nil, err
		}
	}

	// Preserve input order; an earlier SAVED root owns only versioned leaves.
	keep := make([]bool, len(files))
	for i, file := range files {
		keep[i] = true
		for _, root := range earlier {
			if root.locationID == 0 {
				if !underSelection(targets[i], root.path) || root.scope == entity.FileScope_FILE_SCOPE_SAVED && !versions[file.ID] {
					continue
				}
			} else {
				original := originals[file.ID].Original
				if original == nil || original.LocationID != root.locationID || !underSelection(original.Path, root.path) {
					continue
				}
			}
			keep[i] = false
			break
		}
	}
	return keep, nil
}

func (l *Library) savedSelectionFiles(ctx context.Context, ids []int64) (map[int64]bool, error) {
	versions := make(map[int64]bool)
	ids = uniqueFileIDs(ids)
	for start := 0; start < len(ids); start += batchSize {
		var selected []int64
		if err := l.readDB().WithContext(ctx).Model(&FileVersion{}).Distinct("file_id").Where("file_id IN ?", ids[start:min(start+batchSize, len(ids))]).Pluck("file_id", &selected).Error; err != nil {
			return nil, err
		}
		for _, id := range selected {
			versions[id] = true
		}
	}
	return versions, nil
}

// A preceding SAVED root covers only another SAVED traversal, never unbacked leaves.
func selectionTreeCovered(target string, scope entity.FileScope, earlier []selectionRoot) bool {
	for _, root := range earlier {
		if root.locationID == 0 && underSelection(target, root.path) &&
			(root.scope == entity.FileScope_FILE_SCOPE_ALL || scope == entity.FileScope_FILE_SCOPE_SAVED) {
			return true
		}
	}
	return false
}

func (l *Library) walkSelectionTree(ctx context.Context, file *File, target string, scope entity.FileScope, depth int, excludeTrash bool, earlier []selectionRoot, query *FilesQuery, yield func(*File, string) error) error {
	// Exclude unversioned leaves before yielding; logical directories remain traversable.
	if excludeTrash && file.ID == TrashFileID {
		return nil
	}
	if depth > maxFilePathDepth {
		return fmt.Errorf("Library selection exceeds maximum depth")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if selectionTreeCovered(target, scope, earlier) {
		return nil
	}
	if file.Kind != entity.FileKind_FILE_KIND_DIRECTORY {

		if err := entity.ValidateRelativePath(target); err != nil {
			return err
		}
		return yield(file, target)
	}

	// Keep one bounded child page per active depth, with visibility applied in the database.
	cursor := ""
	for {
		page, err := l.ListFileQueryRows(ctx, file.ID, scope, false, query, cursor, batchSize)
		if err != nil {
			return err
		}
		for _, child := range page.Files {
			if err := l.walkSelectionTree(ctx, child, path.Join(target, child.Name), page.Scope, depth+1, excludeTrash, earlier, query, yield); err != nil {
				return err
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

func (l *Library) walkPhysicalSelection(ctx context.Context, root selectionRoot, earlier []selectionRoot, yield func(*File, string) error) error {
	// The published path index supplies descendants; no live filesystem traversal participates.
	base := l.readDB().WithContext(ctx).Where("location_id = ?", root.locationID)
	if root.path != "" {
		base = base.Where("path = ? OR (path >= ? AND path < ?)", root.path, root.path+"/", root.path+"0")
	}
	base = base.Session(&gorm.Session{})
	for _, previous := range earlier {
		if previous.locationID != root.locationID || !underSelection(root.path, previous.path) {
			continue
		}
		// Preserve the explicit missing-path error without loading a covered subtree again.
		if root.path == "" {
			return nil
		}
		var exists []int64
		if err := base.Model(&FileLocation{}).Limit(1).Pluck("file_id", &exists).Error; err != nil {
			return err
		}
		if len(exists) == 0 {
			return fmt.Errorf("selected Location path has no indexed files: %q", root.path)
		}
		return nil
	}

	after, inclusive, found := "", false, false
	for {
		query := base.Where("path > ?", after)
		if inclusive {
			query = base.Where("path >= ?", after)
		}
		var originals []*FileLocation
		if err := query.Order("path").Limit(batchSize).Find(&originals).Error; err != nil {
			return err
		}
		if len(originals) == 0 {
			break
		}
		found = true
		// Jump beyond an earlier nested root using the binary path index, not prefix rescans.
		next, nextInclusive := originals[len(originals)-1].Path, false
		remaining := originals[:0]
		for _, original := range originals {
			covered := false
			for _, previous := range earlier {
				if previous.locationID != root.locationID || !underSelection(original.Path, previous.path) {
					continue
				}
				covered = true
				if end := previous.path + "0"; original.Path != previous.path && end > next {
					next, nextInclusive = end, true
				}
				break
			}
			if !covered {
				remaining = append(remaining, original)
			}
		}
		originals = remaining
		ids := make([]int64, 0, len(originals))
		for _, original := range originals {
			ids = append(ids, original.FileID)
		}
		byID, err := l.ReadFileRows(ctx, ids)
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
		}
		after, inclusive = next, nextInclusive
	}

	if !found && root.path != "" {
		return fmt.Errorf("selected Location path has no indexed files: %q", root.path)
	}
	return nil
}

// MatchSelectionFiles checks a bounded association batch against logical selection roots.
func (l *Library) MatchSelectionFiles(ctx context.Context, ids []int64, selections []*entity.FileSelection) (map[int64]bool, error) {
	result := make(map[int64]bool, len(ids))
	if len(ids) == 0 || len(selections) == 0 {
		return result, nil
	}
	roots := make(map[entity.FileScope][]int64)
	for _, selection := range selections {
		if selection.GetLibrary() == nil {
			continue
		}
		scope, err := l.ResolveFileScope(ctx, selection.Scope)
		if err != nil {
			return nil, err
		}
		roots[scope] = append(roots[scope], selection.GetLibrary().FileId)
	}

	// ALL and SAVED each need one membership read, bounded by this batch and its ancestors.
	for scope, selected := range roots {
		whole := false
		for _, id := range selected {
			if id == 0 {
				whole = true
				break
			}
		}
		query := filterFileScope(l.readDB().WithContext(ctx).Model(ModelFile), scope).Where("files.id IN ?", ids)
		if !whole {
			query = query.Where(`files.id IN (WITH RECURSIVE ancestors(id, parent_id, leaf) AS (
    SELECT id, parent_id, id FROM files WHERE id IN ?
    UNION SELECT parent.id, parent.parent_id, ancestors.leaf FROM files parent JOIN ancestors ON parent.id = ancestors.parent_id
   ) SELECT leaf FROM ancestors WHERE id IN ?)`, ids, selected)
		}
		var matched []int64
		if err := query.Pluck("files.id", &matched).Error; err != nil {
			return nil, err
		}
		for _, id := range matched {
			result[id] = true
		}
	}
	return result, nil
}
