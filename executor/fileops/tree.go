package fileops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/internal/treeops"
	"github.com/samuelncui/yatm/library"
)

type locationTree struct {
	op       *operation
	location *library.Location
	config   *config
}

func (s *locationTree) Stat(ctx context.Context, ref string) (treeops.Node, error) {
	// The reference is interpreted only within the already acquired Location binding.
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
	guard, _ := json.Marshal(executor.LocationFacts(info))
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
		Directory: info.IsDir(), Exists: true, Guard: guard, Size: size}
}

func (s *locationTree) ListChildren(ctx context.Context, parent treeops.Node, cursor string) ([]treeops.Node, string, error) {
	// Reopen a bounded directory page only while its full observation remains unchanged.
	current, err := s.Stat(ctx, parent.Ref)
	if err != nil {
		return nil, "", err
	}
	if !current.Directory || !bytes.Equal(current.Guard, parent.Guard) {
		return nil, "", fmt.Errorf("directory changed: %q", parent.Path)
	}
	directory, err := os.Open(filepath.Join(s.location.RootPath, filepath.FromSlash(parent.Path)))
	if err != nil {
		return nil, "", err
	}
	defer directory.Close()
	offset, err := strconv.Atoi("0" + cursor)
	if err != nil {
		return nil, "", fmt.Errorf("invalid directory cursor, %w", err)
	}
	for skipped := 0; skipped < offset; {
		count := treeops.PageSize
		if offset-skipped < count {
			count = offset - skipped
		}
		rows, err := directory.ReadDir(count)
		if err != nil {
			return nil, "", fmt.Errorf("directory changed while paging, %w", err)
		}
		skipped += len(rows)
	}
	entries, readErr := directory.ReadDir(treeops.PageSize)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, "", readErr
	}
	result := make([]treeops.Node, 0, len(entries))
	for _, entry := range entries {
		node, err := s.Stat(ctx, "path:"+path.Join(parent.Path, entry.Name()))
		if err != nil {
			return nil, "", err
		}
		if !node.Exists {
			return nil, "", fmt.Errorf("directory member disappeared")
		}
		result = append(result, node)
	}
	finished, err := s.Stat(ctx, parent.Ref)
	if err != nil {
		return nil, "", err
	}
	if !bytes.Equal(finished.Guard, parent.Guard) {
		return nil, "", fmt.Errorf("directory changed while paging")
	}
	if errors.Is(readErr, io.EOF) || len(entries) < treeops.PageSize {
		return result, "", nil
	}
	return result, strconv.Itoa(offset + len(entries)), nil
}

func (s *locationTree) ResolveChild(ctx context.Context, parent treeops.Node, name string, _ bool) (treeops.Node, error) {
	// The engine supplies a single component; mounted filesystem lookup owns name equivalence.
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return treeops.Node{}, fmt.Errorf("invalid child name")
	}
	relative := name
	if parent.Path != "" {
		relative = parent.Path + "/" + name
	}
	absent := treeops.Node{Ref: "path:" + relative, Parent: parent.Ref, ParentGuard: parent.Guard, Name: name, Path: relative}
	if err := s.op.protectedRange(ctx, s.location, relative, true); err != nil {
		return treeops.Node{}, err
	}
	if !parent.Exists {
		return absent, nil
	}
	node, err := s.Stat(ctx, absent.Ref)
	if err != nil {
		return treeops.Node{}, err
	}
	if !node.Exists {
		return absent, nil
	}
	node.ParentGuard = parent.Guard
	return node, nil
}

func (s *locationTree) ApplyPrimitive(ctx context.Context, p treeops.Primitive) (treeops.Receipt, error) {
	// The shared planner owns recursion; this boundary changes only one actual directory entry.
	if err := s.op.checkDestination(s.location, s.config.Spec.Destination); err != nil {
		return treeops.Receipt{}, err
	}
	if p.Target.Parent != "" && len(p.Target.ParentGuard) != 0 {
		_, info, err := s.op.exe.CheckLocationPath(s.location, strings.TrimPrefix(p.Target.Parent, "path:"))
		if err != nil {
			return treeops.Receipt{}, err
		}
		var facts entity.LocationFileFacts
		if err := json.Unmarshal(p.Target.ParentGuard, &facts); err != nil {
			return treeops.Receipt{}, err
		}
		if !factsMatch(&facts, info, true) {
			return treeops.Receipt{}, fmt.Errorf("target parent changed")
		}
	}
	if p.Kind == treeops.RemoveEmpty && p.Target.Exists {
		_, info, err := s.op.exe.CheckLocationPath(s.location, p.Target.Path)
		if err != nil {
			return treeops.Receipt{}, err
		}
		var expected entity.LocationFileFacts
		if err := json.Unmarshal(p.Target.Guard, &expected); err != nil {
			return treeops.Receipt{}, err
		}
		if !factsMatch(&expected, info, true) {
			return treeops.Receipt{}, fmt.Errorf("merge destination changed")
		}
	}
	kind := entity.FileOperationKind_MOVE
	switch p.Kind {
	case treeops.Mkdir:
		kind = entity.FileOperationKind_MAKE_DIRECTORY
	case treeops.Delete, treeops.RemoveEmpty:
		kind = entity.FileOperationKind_DELETE
	case treeops.Move:
	default:
		return treeops.Receipt{}, fmt.Errorf("unsupported physical primitive")
	}
	item := &Item{ID: p.ID, SourcePath: p.Source.Path, TargetPath: p.Target.Path, Directory: p.Source.Directory}
	if kind == entity.FileOperationKind_DELETE {
		item.TargetPath = ""
	}
	if len(p.Source.Guard) != 0 {
		if err := json.Unmarshal(p.Source.Guard, &item.Facts); err != nil {
			return treeops.Receipt{}, err
		}
	}
	if err := s.op.mutate(ctx, s.location, kind, item); err != nil {
		return treeops.Receipt{Changed: item.PhysicalDone, PublicationPending: item.PhysicalDone}, err
	}
	receipt := treeops.Receipt{Changed: true}
	if kind != entity.FileOperationKind_DELETE {
		current, err := s.Stat(ctx, p.Target.Ref)
		if err != nil {
			return treeops.Receipt{Changed: true, PublicationPending: true}, err
		}
		receipt.Node = current
	}

	// Physical success settles original references even if the client cancels during publication.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result := &library.FileOperationResult{OperationID: s.config.OperationID, ItemID: p.ID, LocationID: s.location.ID,
		BindingToken: s.config.BindingToken, Kind: kind, SourcePath: item.SourcePath, TargetPath: item.TargetPath,
		OutputIdentity: item.ResultFacts.GetIdentity()}
	publicationErr := s.op.exe.Lib().PublishFileOperation(cleanup, result)
	if publicationErr != nil && cleanup.Err() == nil {
		publicationErr = s.op.exe.Lib().PublishFileOperation(cleanup, result)
	}
	if publicationErr != nil {
		receipt.PublicationPending = true
		return receipt, fmt.Errorf("physical change completed; Library publication failed: %w", publicationErr)
	}
	return receipt, nil
}
