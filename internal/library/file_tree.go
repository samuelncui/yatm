package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/samuelncui/yatm/internal/treeops"
	"gorm.io/gorm"
)

// FileTree exposes logical storage primitives to the shared organization engine.
func (l *Library) FileTree() treeops.Store { return &fileTree{lib: l, db: l.readDB()} }

// FileTreeTransaction retains metadata-only rollback for a selected Library root.
func (l *Library) FileTreeTransaction(ctx context.Context, use func(treeops.Store) error) error {
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return use(&fileTree{lib: &Library{db: tx}, db: tx}) })
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
	// The request owns its temporary plan until execution and result delivery finish.
	temporary, err := resource.OpenTemporaryDB("", "yatm-logical-operation-")
	if err != nil {
		return 0, err
	}
	defer func() { returnErr = errors.Join(returnErr, temporary.Close()) }()

	// Keep the plan schema and logical transaction policy with the existing organization engine.
	engine, err := treeops.New(ctx, temporary.DB, l.FileTree(), treeops.Options{NativeMove: true}, l.FileTreeTransaction)
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
	var row fileRow
	err = s.db.WithContext(ctx).First(&row, id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return treeops.Node{Ref: ref}, nil
	}
	if err != nil {
		return treeops.Node{}, err
	}

	// Derive display paths using bounded ancestry; the stable reference remains the File ID.
	names := []string{row.Name}
	seen := map[int64]struct{}{row.ID: {}}
	for parentID := row.ParentID; parentID != 0; {
		if _, found := seen[parentID]; found {
			return treeops.Node{}, fmt.Errorf("Library ancestry contains a cycle")
		}
		if len(seen) >= 256 {
			return treeops.Node{}, fmt.Errorf("Library ancestry exceeds 256 levels")
		}
		seen[parentID] = struct{}{}
		var parent fileRow
		if err := s.db.WithContext(ctx).First(&parent, parentID).Error; err != nil {
			return treeops.Node{}, err
		}
		names = append(names, parent.Name)
		parentID = parent.ParentID
	}
	for left, right := 0, len(names)-1; left < right; left, right = left+1, right-1 {
		names[left], names[right] = names[right], names[left]
	}
	node := logicalNode(row.file(), strings.Join(names, "/"))
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

func (s *fileTree) WalkChildren(ctx context.Context, parent treeops.Node, visit func(treeops.Node) error) error {
	// Logical traversal retains stable File-ID pages without materializing a complete directory.
	if !parent.Exists || !parent.Directory {
		return fmt.Errorf("source is not a directory")
	}
	parentID, err := strconv.ParseInt(parent.Ref, 10, 64)
	if err != nil {
		return err
	}
	var after int64
	for {
		var rows []*fileRow
		if err := s.db.WithContext(ctx).Where("parent_id = ? AND id > ?", parentID, after).
			Order("id").Limit(treeops.PageSize).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := visit(logicalNode(row.file(), displayChild(parent.Path, row.Name))); err != nil {
				return err
			}
		}
		if len(rows) < treeops.PageSize {
			return nil
		}
		after = rows[len(rows)-1].ID
	}
}

func displayChild(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}

func (s *fileTree) ResolveChild(ctx context.Context, parent treeops.Node, name string, _ bool) (treeops.Node, error) {
	// Only ordinary names reach this boundary; relative shortcuts are resolved by the engine.
	if err := entity.ValidatePathComponent(name); err != nil {
		return treeops.Node{}, fmt.Errorf("invalid logical child name, %w", err)
	}

	// Missing ancestors defer lookup until the shared plan creates them.
	childPath := displayChild(parent.Path, name)
	absent := treeops.Node{Ref: "path:" + childPath, Parent: parent.Ref, ParentGuard: parent.Guard, Name: name, Path: childPath}
	if !parent.Exists {
		return absent, nil
	}

	// Match an existing child by its exact name under the stored parent identity.
	id, err := strconv.ParseInt(parent.Ref, 10, 64)
	if err != nil {
		return treeops.Node{}, err
	}
	var row fileRow
	err = s.db.WithContext(ctx).Where("parent_id = ? AND name = ?", id, name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return absent, nil
	}
	if err != nil {
		return treeops.Node{}, err
	}
	result := logicalNode(row.file(), childPath)
	result.ParentGuard = parent.Guard
	return result, nil
}

func (s *fileTree) ApplyPrimitive(ctx context.Context, p treeops.Primitive) (treeops.Receipt, error) {
	source := p.Source
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
		if err := createFileRow(s.db.WithContext(ctx), &file); err != nil {
			return treeops.Receipt{}, err
		}
	case treeops.Move:
		var row fileRow
		if err := s.db.WithContext(ctx).First(&row, source.FileID).Error; err != nil {
			return treeops.Receipt{}, err
		}
		file = *row.file()
		file.ParentID, file.Name = parentID, p.Target.Name
		if err := saveFileRow(s.db.WithContext(ctx), &file); err != nil {
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
	if err := s.db.WithContext(ctx).Model(ModelFile).Where("parent_id = ?", source.FileID).Count(&count).Error; err != nil {
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
	var fromRow, toRow fileRow
	if err := s.db.WithContext(ctx).First(&fromRow, source.FileID).Error; err != nil {
		return treeops.Receipt{}, err
	}
	if err := s.db.WithContext(ctx).First(&toRow, current.FileID).Error; err != nil {
		return treeops.Receipt{}, err
	}
	from, to := fromRow.file(), toRow.file()
	if _, err := s.lib.mergeFileMetadata(ctx, s.db, from, to); err != nil {
		return treeops.Receipt{}, err
	}
	if err := saveFileRow(s.db.WithContext(ctx), to); err != nil {
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
	var row fileRow
	if err := s.db.WithContext(ctx).First(&row, source.FileID).Error; err != nil {
		return treeops.Receipt{}, err
	}
	file := row.file()
	if err := placeInTrash(ctx, s.db, trash.ID, []*File{file}); err != nil {
		return treeops.Receipt{}, err
	}
	return treeops.Receipt{Node: source, Changed: true}, nil
}

// placeInTrash retains names and identities under independently allocated File-ID containers.
// The caller owns the checkpoint, transaction and any history/original retirement policy.
func placeInTrash(ctx context.Context, tx *gorm.DB, checkpointID int64, files []*File) error {
	// Bound name lookup and directory creation even when a Merge visitor supplies a larger page.
	for start := 0; start < len(files); start += batchSize {
		batch := files[start:min(start+batchSize, len(files))]
		names := make([]string, 0, len(batch))
		for _, file := range batch {
			if file == nil || file.ID <= 0 {
				return fmt.Errorf("Trash placement requires stored Files")
			}
			names = append(names, strconv.FormatInt(file.ID, 10))
		}
		var occupied []string
		if err := tx.WithContext(ctx).Model(&fileRow{}).Where("parent_id = ? AND name IN ?", checkpointID, names).
			Pluck("name", &occupied).Error; err != nil {
			return err
		}
		used := make(map[string]bool, len(occupied))
		for _, name := range occupied {
			used[name] = true
		}

		// Existing user organization is never reused or overwritten, including an old empty container.
		folders := make([]fileRow, 0, len(batch))
		for _, name := range names {
			candidate := name
			for suffix := 2; used[candidate]; suffix++ {
				candidate = fmt.Sprintf("%s (%d)", name, suffix)
				var count int64
				if err := tx.WithContext(ctx).Model(&fileRow{}).Where("parent_id = ? AND name = ?", checkpointID, candidate).Count(&count).Error; err != nil {
					return err
				}
				used[candidate] = count != 0
			}
			used[candidate] = true
			folders = append(folders, fileRow{ParentID: checkpointID, Name: candidate, Kind: entity.FileKind_FILE_KIND_DIRECTORY})
		}
		if err := tx.WithContext(ctx).Create(&folders).Error; err != nil {
			return err
		}

		// Only the parent changes; physical originals, saved history and annotations belong to callers.
		for index, file := range batch {
			if err := tx.WithContext(ctx).Model(&fileRow{}).Where("id = ?", file.ID).Update("parent_id", folders[index].ID).Error; err != nil {
				return err
			}
			file.ParentID = folders[index].ID
		}
	}
	return nil
}
