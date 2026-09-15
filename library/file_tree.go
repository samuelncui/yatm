package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/treeops"
	"github.com/samuelncui/yatm/resource"
	"gorm.io/gorm"
)

// FileTree exposes logical storage primitives to the shared organization engine.
func (l *Library) FileTree() treeops.Store { return &fileTree{lib: l, db: l.db} }

// FileTreeTransaction retains metadata-only rollback for a selected Library root.
func (l *Library) FileTreeTransaction(ctx context.Context, use func(treeops.Store) error) error {
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return use(&fileTree{lib: l, db: tx}) })
}

// moveTree keeps direct Library callers on the same organization engine as Files.
func (l *Library) moveTree(ctx context.Context, file *File, name string) error {
	// Validate the selected identity before allocating the bounded temporary plan.
	if file == nil || file.ID <= 0 {
		return fmt.Errorf("move requires a stored File")
	}
	if _, err := l.GetFile(ctx, file.ID); err != nil {
		return err
	}
	resultID, err := l.organizeTree(ctx, treeops.Request{Kind: treeops.Move, Sources: []string{strconv.FormatInt(file.ID, 10)}, Destination: strconv.FormatInt(file.ParentID, 10), Name: name})
	if err != nil {
		return err
	}
	stored, err := l.GetFile(ctx, resultID)
	if err != nil {
		return err
	}
	*file = *stored
	return nil
}

// organizeTree freezes the plan outside metadata transactions and runs shared tree rules.
func (l *Library) organizeTree(ctx context.Context, request treeops.Request) (resultID int64, returnErr error) {
	directory, err := os.MkdirTemp("", "yatm-logical-operation-")
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, os.RemoveAll(directory)) }()
	db, err := resource.OpenSQLite(filepath.Join(directory, "manifest.db"))
	if err != nil {
		return 0, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, sqlDB.Close()) }()
	engine, err := treeops.New(ctx, db, l.FileTree(), treeops.Options{NativeMove: true}, l.FileTreeTransaction)
	if err != nil {
		return 0, err
	}
	if err := engine.Prepare(ctx, request); err != nil {
		return 0, err
	}

	// Only expose the surviving identity after its complete logical transaction committed.
	if err := engine.Run(ctx, func(result treeops.Result) error {
		if result.Outcome != treeops.Succeeded {
			return fmt.Errorf("logical operation failed: %s", result.Error)
		}
		if result.FileID != 0 {
			resultID = result.FileID
		}
		return nil
	}); err != nil {
		return 0, err
	}
	return resultID, nil
}

type fileTree struct {
	lib *Library
	db  *gorm.DB
}

func (s *fileTree) Stat(ctx context.Context, ref string) (treeops.Node, error) {
	// Pending paths resolve only within the logical root, never against a physical namespace.
	if strings.HasPrefix(ref, "path:") {
		value := strings.TrimPrefix(ref, "path:")
		parent := treeops.Node{Ref: "0", Exists: true, Directory: true}
		for _, name := range strings.Split(value, "/") {
			next, err := s.ResolveChild(ctx, parent, name, true)
			if err != nil {
				return treeops.Node{}, err
			}
			parent = next
		}
		return parent, nil
	}
	if ref == "0" {
		return treeops.Node{Ref: "0", Exists: true, Directory: true}, nil
	}
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil {
		return treeops.Node{}, fmt.Errorf("invalid logical reference, %w", err)
	}
	var file File
	err = s.db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).First(&file, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return treeops.Node{Ref: ref}, nil
	}
	if err != nil {
		return treeops.Node{}, err
	}

	// Derive display paths using bounded ancestry; the stable reference remains the File ID.
	names := []string{file.Name}
	seen := map[int64]struct{}{file.ID: {}}
	for parentID := file.ParentID; parentID != 0; {
		if _, found := seen[parentID]; found {
			return treeops.Node{}, fmt.Errorf("Library ancestry contains a cycle")
		}
		if len(seen) >= 256 {
			return treeops.Node{}, fmt.Errorf("Library ancestry exceeds 256 levels")
		}
		seen[parentID] = struct{}{}
		var parent File
		if err := s.db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).First(&parent, parentID).Error; err != nil {
			return treeops.Node{}, err
		}
		names = append(names, parent.Name)
		parentID = parent.ParentID
	}
	for left, right := 0, len(names)-1; left < right; left, right = left+1, right-1 {
		names[left], names[right] = names[right], names[left]
	}
	node := logicalNode(&file, strings.Join(names, "/"))
	if _, inTrash := seen[TrashFileID]; inTrash {
		node.Deletion = treeops.Retain
		if node.Directory {
			node.Deletion = treeops.EmptyDirectories
		}
	}
	return node, nil
}

func logicalNode(file *File, display string) treeops.Node {
	guard, _ := json.Marshal(struct {
		ID, ParentID int64
		Name         string
		Kind         entity.FileKind
	}{file.ID, file.ParentID, file.Name, file.Kind})
	return treeops.Node{Ref: strconv.FormatInt(file.ID, 10), Parent: strconv.FormatInt(file.ParentID, 10), Name: file.Name,
		Path: display, Exists: true, Directory: file.Kind == entity.FileKind_FILE_KIND_DIRECTORY, Guard: guard, FileID: file.ID,
		Protected: file.ID <= 0, Deletion: treeops.Detach}
}

func (s *fileTree) ListChildren(ctx context.Context, parent treeops.Node, cursor string) ([]treeops.Node, string, error) {
	// Stable File-ID pages ignore unrelated annotation edits and never load a whole directory.
	current, err := s.Stat(ctx, parent.Ref)
	if err != nil {
		return nil, "", err
	}
	if !current.Exists || !current.Directory || !bytes.Equal(current.Guard, parent.Guard) {
		return nil, "", fmt.Errorf("directory changed")
	}
	after, _ := strconv.ParseInt(cursor, 10, 64)
	parentID, _ := strconv.ParseInt(current.Ref, 10, 64)
	var files []File
	if err := s.db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Where("parent_id = ? AND id > ?", parentID, after).
		Order("id").Limit(treeops.PageSize).Find(&files).Error; err != nil {
		return nil, "", err
	}
	nodes := make([]treeops.Node, 0, len(files))
	for _, file := range files {
		nodes = append(nodes, logicalNode(&file, displayChild(current.Path, file.Name)))
	}
	if len(files) < treeops.PageSize {
		return nodes, "", nil
	}
	return nodes, strconv.FormatInt(files[len(files)-1].ID, 10), nil
}

func displayChild(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

func (s *fileTree) ResolveChild(ctx context.Context, parent treeops.Node, name string, _ bool) (treeops.Node, error) {
	// Only ordinary names reach this boundary; relative shortcuts are resolved by the engine.
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return treeops.Node{}, fmt.Errorf("invalid logical child name")
	}
	childPath := displayChild(parent.Path, name)
	absent := treeops.Node{Ref: "path:" + childPath, Parent: parent.Ref, ParentGuard: parent.Guard, Name: name, Path: childPath}
	if !parent.Exists {
		return absent, nil
	}
	id, err := strconv.ParseInt(parent.Ref, 10, 64)
	if err != nil {
		return treeops.Node{}, err
	}
	var file File
	err = s.db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Where("parent_id = ? AND name = ?", id, name).First(&file).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return absent, nil
	}
	if err != nil {
		return treeops.Node{}, err
	}
	result := logicalNode(&file, childPath)
	result.ParentGuard = parent.Guard
	return result, nil
}

func (s *fileTree) ApplyPrimitive(ctx context.Context, p treeops.Primitive) (treeops.Receipt, error) {
	// Guard structural identity while retaining concurrently edited annotations.
	source, err := s.Stat(ctx, p.Source.Ref)
	if !p.Source.Exists {
		source, err = treeops.Node{}, nil
	}
	if err != nil {
		return treeops.Receipt{}, err
	}
	if p.Source.Exists && (!source.Exists || !bytes.Equal(source.Guard, p.Source.Guard)) {
		return treeops.Receipt{}, fmt.Errorf("Library source changed")
	}
	if p.Kind == treeops.Delete {
		return s.detach(ctx, source)
	}
	if p.Kind == treeops.RemoveEmpty {
		return s.removeEmpty(ctx, source, p.Target)
	}
	parent, err := s.Stat(ctx, p.Target.Parent)
	if err != nil {
		return treeops.Receipt{}, err
	}
	if !parent.Exists || !parent.Directory {
		return treeops.Receipt{}, fmt.Errorf("target parent disappeared")
	}
	if len(p.Target.ParentGuard) != 0 && !bytes.Equal(parent.Guard, p.Target.ParentGuard) {
		return treeops.Receipt{}, fmt.Errorf("target parent changed")
	}
	target, err := s.ResolveChild(ctx, parent, p.Target.Name, p.Target.Directory)
	if err != nil {
		return treeops.Receipt{}, err
	}
	if target.Exists {
		return treeops.Receipt{}, fmt.Errorf("target appeared: %q", target.Path)
	}
	parentID, _ := strconv.ParseInt(parent.Ref, 10, 64)
	if err := validateFileParent(s.db, parentID, source.FileID); err != nil {
		return treeops.Receipt{}, err
	}

	// Unique parent/name constraints provide the no-replace check at the actual write boundary.
	var file File
	switch p.Kind {
	case treeops.Mkdir:
		file = File{ParentID: parentID, Name: p.Target.Name, Kind: entity.FileKind_FILE_KIND_DIRECTORY}
		if err := s.db.WithContext(ctx).Create(&file).Error; err != nil {
			return treeops.Receipt{}, err
		}
	case treeops.Move:
		if err := s.db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).First(&file, source.FileID).Error; err != nil {
			return treeops.Receipt{}, err
		}
		file.ParentID, file.Name = parentID, p.Target.Name
		if err := s.db.WithContext(ctx).Save(&file).Error; err != nil {
			return treeops.Receipt{}, err
		}
	default:
		return treeops.Receipt{}, fmt.Errorf("unsupported logical primitive")
	}
	return treeops.Receipt{Node: logicalNode(&file, p.Target.Path), Changed: true}, nil
}

func (s *fileTree) removeEmpty(ctx context.Context, source, target treeops.Node) (treeops.Receipt, error) {
	// Merge metadata only after the shared engine moved all children into the surviving directory.
	var count int64
	if err := s.db.WithContext(ctx).Model(&File{}).Where("parent_id = ?", source.FileID).Count(&count).Error; err != nil {
		return treeops.Receipt{}, err
	}
	if count != 0 {
		return treeops.Receipt{}, fmt.Errorf("source directory is not empty")
	}
	if target.Ref == "" {
		if err := deleteFileRows(ctx, s.db, []int64{source.FileID}); err != nil {
			return treeops.Receipt{}, err
		}
		return treeops.Receipt{Changed: true, Node: source}, nil
	}
	current, err := s.Stat(ctx, target.Ref)
	if err != nil {
		return treeops.Receipt{}, err
	}
	if !current.Exists || !current.Directory {
		return treeops.Receipt{}, fmt.Errorf("merge destination disappeared")
	}
	if target.Exists && !bytes.Equal(current.Guard, target.Guard) {
		return treeops.Receipt{}, fmt.Errorf("merge destination changed")
	}
	var from, to File
	if err := s.db.WithContext(ctx).First(&from, source.FileID).Error; err != nil {
		return treeops.Receipt{}, err
	}
	if err := s.db.WithContext(ctx).First(&to, current.FileID).Error; err != nil {
		return treeops.Receipt{}, err
	}
	if _, err := s.lib.mergeFileMetadata(ctx, s.db, &from, &to); err != nil {
		return treeops.Receipt{}, err
	}
	if err := s.db.WithContext(ctx).Save(&to).Error; err != nil {
		return treeops.Receipt{}, err
	}
	if err := deleteFileRows(ctx, s.db, []int64{from.ID}); err != nil {
		return treeops.Receipt{}, err
	}
	return treeops.Receipt{Node: current, Changed: true}, nil
}

func (s *fileTree) detach(ctx context.Context, source treeops.Node) (treeops.Receipt, error) {
	// Trash is a logical detach, not recursive deletion of bytes or saved file identities.
	if source.FileID <= 0 {
		return treeops.Receipt{}, fmt.Errorf("Library root is protected")
	}
	trash, err := s.lib.newTrash(ctx, s.db)
	if err != nil {
		return treeops.Receipt{}, err
	}
	var file File
	if err := s.db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).First(&file, source.FileID).Error; err != nil {
		return treeops.Receipt{}, err
	}
	file.ParentID = trash.ID
	if err := s.db.WithContext(ctx).Save(&file).Error; err != nil {
		return treeops.Receipt{}, err
	}
	return treeops.Receipt{Node: source, Changed: true}, nil
}
