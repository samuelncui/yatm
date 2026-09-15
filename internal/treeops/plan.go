package treeops

import (
	"context"
	"errors"
	"fmt"
)

var errRetainedFile = errors.New("Trash subtree contains retained Files")

// Prepare freezes every selected root before changing either storage namespace.
func (e *Engine) Prepare(ctx context.Context, request Request) error {
	// Resolve source identity and overlapping ancestors without relying on path spelling.
	selected := make(map[string]Node, len(request.Sources))
	var sources []Node
	for _, ref := range request.Sources {
		node, err := e.store.Stat(ctx, ref)
		if err != nil {
			return err
		}
		if !node.Exists || node.Parent == "" || node.Protected {
			return fmt.Errorf("source is missing or is a protected root")
		}
		if _, found := selected[node.Ref]; found {
			continue
		}
		selected[node.Ref] = node
		sources = append(sources, node)
	}
	var destination Node
	if request.Kind != Delete {
		var err error
		destination, err = e.store.Stat(ctx, request.Destination)
		if err != nil {
			return err
		}
		if !destination.Exists || !destination.Directory {
			return fmt.Errorf("destination is not a directory")
		}
	}
	if request.Kind == Mkdir {
		sources = []Node{{}}
	}

	// Each root owns its preflight outcome; failed plans are never executed.
	for _, source := range sources {
		covered, err := e.covered(ctx, source.Parent, selected)
		if err != nil {
			return err
		}
		if covered {
			continue
		}
		root := &Root{Source: source}
		if err := e.db.WithContext(ctx).Create(root).Error; err != nil {
			return err
		}
		err = e.planRoot(ctx, root, request, destination)
		if err != nil {
			root.Error = err.Error()
			if err := e.db.WithContext(ctx).Where("root_id = ?", root.ID).Delete(&step{}).Error; err != nil {
				return err
			}
			if err := e.db.WithContext(ctx).Create(&step{RootID: root.ID, Source: root.Source, Target: root.Target, Outcome: Failed, Error: root.Error}).Error; err != nil {
				return err
			}
		} else {
			var count int64
			if err := e.db.WithContext(ctx).Model(&step{}).Where("root_id = ? AND check_only = ?", root.ID, false).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 {
				if err := e.add(ctx, root.ID, "", root.Source, root.Target, false); err != nil {
					return err
				}
			}
		}
		if err := e.db.WithContext(ctx).Save(root).Error; err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) covered(ctx context.Context, parent string, selected map[string]Node) (bool, error) {
	seen := make(map[string]struct{})
	for parent != "" {
		if _, found := selected[parent]; found {
			return true, nil
		}
		if _, found := seen[parent]; found {
			return false, fmt.Errorf("directory ancestry contains a cycle")
		}
		if len(seen) >= 256 {
			return false, fmt.Errorf("directory ancestry exceeds 256 levels")
		}
		seen[parent] = struct{}{}
		node, err := e.store.Stat(ctx, parent)
		if err != nil {
			return false, err
		}
		parent = node.Parent
	}
	return false, nil
}

func (e *Engine) planRoot(ctx context.Context, root *Root, request Request, destination Node) error {
	// Reject moving into a selected source before allocating intermediate directories.
	if request.Kind == Delete {
		return e.planDelete(ctx, root.ID, root.Source, 0)
	}
	if request.Kind == Move {
		inside, err := e.covered(ctx, destination.Ref, map[string]Node{root.Source.Ref: root.Source})
		if err != nil {
			return err
		}
		if inside {
			return fmt.Errorf("cannot move a directory into itself")
		}
	}
	name := request.Name
	if name == "" {
		name = root.Source.Name
	}
	names, err := relativeNames(name, request.Kind == Mkdir)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		root.Target = destination
		return nil
	}

	// Resolve target names through the provider, including case and virtual-directory semantics.
	for index, name := range names {
		directory := request.Kind == Mkdir || index < len(names)-1 || root.Source.Directory
		target, err := e.store.ResolveChild(ctx, destination, name, directory)
		if err != nil {
			return err
		}
		if target.Ref == root.Source.Ref && request.Kind == Move {
			if index != len(names)-1 {
				return fmt.Errorf("cannot move a directory into itself")
			}
			root.Target = target
			return nil
		}
		if index == len(names)-1 && request.Kind == Move {
			root.Target = target
			return e.planMove(ctx, root.ID, root.Source, target, 0)
		}
		if target.Exists && !target.Directory {
			return fmt.Errorf("target is not a directory: %q", target.Path)
		}
		if !target.Exists {
			target.Directory = true
			if err := e.add(ctx, root.ID, Mkdir, Node{}, target, false); err != nil {
				return err
			}
		}
		destination = target
	}
	root.Target = destination
	return nil
}

func (e *Engine) planMove(ctx context.Context, rootID int64, source, target Node, depth int) error {
	// Existing leaf names always conflict, including byte-identical content.
	if target.Exists && (!source.Directory || !target.Directory) {
		return fmt.Errorf("target already exists: %q", target.Path)
	}
	if !source.Directory {
		return e.add(ctx, rootID, Move, source, target, false)
	}
	if !target.Exists && e.options.NativeMove {
		if err := e.observeTree(ctx, rootID, source, depth); err != nil {
			return err
		}
		return e.add(ctx, rootID, Move, source, target, false)
	}

	// Directory merging is the same paged algorithm for every provider.
	if !target.Exists {
		target.Directory = true
		if err := e.add(ctx, rootID, Mkdir, Node{}, target, false); err != nil {
			return err
		}
	}
	if err := e.children(ctx, source, depth, func(child Node) error {
		next, err := e.store.ResolveChild(ctx, target, child.Name, child.Directory)
		if err != nil {
			return err
		}
		return e.planMove(ctx, rootID, child, next, depth+1)
	}); err != nil {
		return err
	}
	return e.add(ctx, rootID, RemoveEmpty, source, target, false)
}

func (e *Engine) planDelete(ctx context.Context, rootID int64, source Node, depth int) error {
	// Logical deletion detaches a single tree identity; physical deletion unlinks only frozen leaves.
	if source.Deletion == Retain {
		return e.add(ctx, rootID, "", source, Node{}, false)
	}
	if source.Deletion == Detach {
		return e.add(ctx, rootID, Delete, source, Node{}, false)
	}
	if source.Deletion == EmptyDirectories {
		err := e.planEmptyDirectories(ctx, rootID, source, depth)
		if !errors.Is(err, errRetainedFile) {
			return err
		}
		if err := e.db.WithContext(ctx).Where("root_id = ?", rootID).Delete(&step{}).Error; err != nil {
			return err
		}
		return e.add(ctx, rootID, "", source, Node{}, false)
	}
	if source.Directory {
		if err := e.children(ctx, source, depth, func(child Node) error { return e.planDelete(ctx, rootID, child, depth+1) }); err != nil {
			return err
		}
	}
	return e.add(ctx, rootID, Delete, source, Node{}, false)
}

func (e *Engine) planEmptyDirectories(ctx context.Context, rootID int64, source Node, depth int) error {
	// Files already in Trash are retained; an all-directory subtree may be removed.
	if !source.Directory {
		return errRetainedFile
	}
	if err := e.children(ctx, source, depth, func(child Node) error { return e.planEmptyDirectories(ctx, rootID, child, depth+1) }); err != nil {
		return err
	}
	return e.add(ctx, rootID, RemoveEmpty, source, Node{}, false)
}

func (e *Engine) observeTree(ctx context.Context, rootID int64, source Node, depth int) error {
	if err := e.add(ctx, rootID, "", source, Node{}, true); err != nil {
		return err
	}
	if !source.Directory {
		return nil
	}
	return e.children(ctx, source, depth, func(child Node) error { return e.observeTree(ctx, rootID, child, depth+1) })
}

func (e *Engine) children(ctx context.Context, source Node, depth int, use func(Node) error) error {
	// Bound both retained directory pages and stack depth.
	if depth >= 256 {
		return fmt.Errorf("operation exceeds 256 directory levels")
	}
	var cursor string
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		children, next, err := e.store.ListChildren(ctx, source, cursor)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := use(child); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		if next == cursor {
			return fmt.Errorf("directory cursor did not advance")
		}
		cursor = next
	}
}

func (e *Engine) add(ctx context.Context, rootID int64, kind Kind, source, target Node, checkOnly bool) error {
	// Reserve complete target slots across roots before any physical mutation begins.
	if target.Ref != "" && kind != RemoveEmpty {
		var count int64
		if err := e.db.WithContext(ctx).Model(&step{}).Where("root_id <> ? AND check_only = ?", rootID, false).
			Where("target_ref = ?", target.Ref).Count(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("selected targets conflict: %q", target.Path)
		}
	}
	return e.db.WithContext(ctx).Create(&step{RootID: rootID, Kind: kind, Source: source, Target: target, TargetRef: target.Ref, CheckOnly: checkOnly, Size: source.Size}).Error
}
