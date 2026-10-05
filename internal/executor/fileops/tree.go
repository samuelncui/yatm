package fileops

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"github.com/samuelncui/yatm/internal/treeops"
)

type locationTree struct {
	op       *operation
	location *library.Location
	config   *config
}

func (s *locationTree) Stat(ctx context.Context, ref string) (treeops.Node, error) {
	// The reference is interpreted only within the already acquired Location boundary.
	if err := ctx.Err(); err != nil {
		return treeops.Node{}, err
	}
	relative := strings.TrimPrefix(ref, "path:")
	_, info, err := s.op.exe.CheckLocationPath(s.location, relative)
	if os.IsNotExist(err) {
		return treeops.Node{Ref: ref, Path: relative}, nil
	}
	if err != nil {
		return treeops.Node{}, err
	}
	if relative != "" {
		if err := s.op.protected(ctx, s.location, relative, info); err != nil {
			return treeops.Node{}, err
		}
	}
	return physicalNode(relative, info), nil
}

func physicalNode(relative string, info os.FileInfo) treeops.Node {
	parentPath := path.Dir(relative)
	if parentPath == "." {
		parentPath = ""
	}
	parent := "path:" + parentPath
	if relative == "" {
		parent = ""
	}
	size := int64(0)
	if info.Mode().IsRegular() {
		size = info.Size()
	}
	return treeops.Node{Ref: "path:" + relative, Parent: parent, Name: info.Name(), Path: relative,
		Directory: info.IsDir(), Exists: true, Size: size, Deletion: treeops.Recycle,
		Protected: executor.IsLocationTrashPath(relative) && !executor.IsLocationTrashContent(relative)}
}

func (s *locationTree) WalkChildren(ctx context.Context, parent treeops.Node, visit func(treeops.Node) error) error {
	// The shared reader owns bounded enumeration and metadata under the prepared parent.
	reader, err := s.op.exe.PrepareLocationDirectory(s.location, parent.Path)
	if err != nil {
		return err
	}

	// Physical traversal includes ignored children; mandatory access and operation guards still apply.
	return reader.ReadEntries(ctx, func(children []os.DirEntry) error {
		rows, infos, err := reader.Observe(ctx, children)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			info := infos[row.Path]
			if !reader.Allowed(info.Name(), info.IsDir()) {
				return executor.ErrAccessExcluded
			}
			if err := s.op.protected(ctx, s.location, row.Path, info); err != nil {
				return err
			}
			if err := visit(physicalNode(row.Path, info)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *locationTree) ResolveChild(ctx context.Context, parent treeops.Node, name string, _ bool) (treeops.Node, error) {
	// The engine supplies a single component; mounted filesystem lookup owns name equivalence.
	if err := entity.ValidatePathComponent(name); err != nil {
		return treeops.Node{}, fmt.Errorf("invalid child name, %w", err)
	}

	// Authorize the literal target even when its planned parent is not present yet.
	relative := name
	if parent.Path != "" {
		relative = parent.Path + "/" + name
	}
	if executor.IsLocationTrashPath(relative) {
		return treeops.Node{}, fmt.Errorf("Trash cannot be an operation destination")
	}
	absent := treeops.Node{Ref: "path:" + relative, Parent: parent.Ref, Name: name, Path: relative}
	if err := s.op.protectedRange(ctx, s.location, relative, true); err != nil {
		return treeops.Node{}, err
	}
	if !parent.Exists {
		return absent, nil
	}

	// Existing entries retain the physical filesystem's own name-equivalence rules.
	node, err := s.Stat(ctx, absent.Ref)
	if err != nil {
		return treeops.Node{}, err
	}
	if !node.Exists {
		return absent, nil
	}
	return node, nil
}

func (s *locationTree) ApplyPrimitive(ctx context.Context, p treeops.Primitive) (treeops.Receipt, error) {
	// Admission stops pending work; an admitted primitive retains values but cannot be interrupted.
	if err := ctx.Err(); err != nil {
		return treeops.Receipt{}, err
	}
	ctx = context.WithoutCancel(ctx)

	// The shared planner owns recursion; this boundary changes only one actual directory entry.
	kind := entity.FileOperationKind_FILE_OPERATION_KIND_MOVE
	switch p.Kind {
	case treeops.Mkdir:
		kind = entity.FileOperationKind_FILE_OPERATION_KIND_MKDIR
	case treeops.Delete, treeops.RemoveEmpty:
		kind = entity.FileOperationKind_FILE_OPERATION_KIND_REMOVE
	case treeops.Move:
	default:
		return treeops.Receipt{}, fmt.Errorf("unsupported physical primitive")
	}
	item := &Item{ID: p.ID, SourcePath: p.Source.Path, TargetPath: p.Target.Path, Directory: p.Source.Directory,
		UnlinkEmpty: p.Kind == treeops.RemoveEmpty}
	if item.UnlinkEmpty {
		item.TargetPath = ""
	}
	if p.Kind == treeops.Delete {
		target, err := s.trashTarget(ctx, item)
		if err != nil {
			return treeops.Receipt{}, err
		}
		item.TargetPath = target
	}
	if err := s.op.mutate(ctx, s.location, kind, item); err != nil {
		return treeops.Receipt{Node: treeops.Node{Ref: "path:" + item.TargetPath, Path: item.TargetPath},
			Changed: item.PhysicalDone, PublicationPending: item.PhysicalDone}, err
	}

	// The successful syscall establishes the target; a second Stat cannot improve its receipt.
	receipt := treeops.Receipt{Changed: true}
	if !item.UnlinkEmpty {
		parent := path.Dir(item.TargetPath)
		if parent == "." {
			parent = ""
		}
		receipt.Node = treeops.Node{Ref: "path:" + item.TargetPath, Path: item.TargetPath,
			Parent: "path:" + parent, Name: path.Base(item.TargetPath), Exists: true,
			Directory: item.Directory || p.Kind == treeops.Mkdir, Size: p.Source.Size, Deletion: treeops.Recycle}
	}

	// Settle originals after physical success without an unrelated cleanup deadline.
	result := &library.FileOperation{LocationID: s.location.ID,
		Kind: kind, SourcePath: item.SourcePath, TargetPath: item.TargetPath}
	publicationErr := s.op.exe.Lib().PublishFileOperation(ctx, result)
	if publicationErr != nil {
		receipt.PublicationPending = true
		return receipt, fmt.Errorf("physical change completed; Library publication failed: %w", publicationErr)
	}
	return receipt, nil
}
