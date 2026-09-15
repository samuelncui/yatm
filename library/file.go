package library

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"

	"github.com/samuelncui/yatm/internal/treeops"
	"time"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

var (
	ModelFile         = new(File)
	ModelFileTag      = new(FileTag)
	SignatureV1Header = []byte{0x01}

	ErrFileNotFound          = fmt.Errorf("get file: file not found")
	ErrMkdirNonDirFileExists = fmt.Errorf("mkdir: non dir exists")
	ErrMkdirDirExists        = fmt.Errorf("mkdir: dir exists")

	Root = &File{ID: 0, Kind: entity.FileKind_FILE_KIND_DIRECTORY}
)

type File struct {
	ID       int64 `gorm:"primaryKey;autoIncrement" json:"id,omitempty"`
	ParentID int64 `gorm:"index:idx_files_parent_name,unique" json:"parent_id,omitempty"`

	Name      string          `gorm:"type:varchar(256);index:idx_files_parent_name,unique;index:idx_files_search_name" json:"name,omitempty"`
	Kind      entity.FileKind `gorm:"not null" json:"kind"`
	CreatedAt int64           `gorm:"autoCreateTime:milli" json:"created_at_ms"`
	UpdatedAt int64           `gorm:"autoUpdateTime:milli" json:"updated_at_ms"`
	// Presentation facts are derived from the original or a saved version, never persisted on File.
	Mode    uint32    `gorm:"-" json:"-"`
	ModTime time.Time `gorm:"-" json:"-"`
	Hash    []byte    `gorm:"-" json:"-"`
	Size    int64     `gorm:"-" json:"-"`
	Note    string    `gorm:"type:varchar(4096);not null;default:''" json:"note,omitempty"`
	Tags    []string  `gorm:"-" json:"tags,omitempty"`

	Signature      []byte                     `gorm:"-" json:"-"`
	ContentSummary *entity.FileContentSummary `gorm:"-" json:"-"`
}

func (file *File) AfterFind(tx *gorm.DB) error {
	return hydrateFileFacts(tx.Session(&gorm.Session{NewDB: true}), file)
}

type FileTag struct {
	FileID int64  `gorm:"primaryKey;autoIncrement:false;index:idx_file_tags_tag_file,priority:2"`
	Tag    string `gorm:"type:varchar(128);primaryKey;index:idx_file_tags_tag_file,priority:1"`
}

// NewFileSignature builds the stable content identity shared by Library files and derived artifacts.
func NewFileSignature(hash []byte, size int64) ([]byte, error) {
	if len(hash) != sha256.Size {
		return nil, fmt.Errorf("invalid file SHA-256 length, length=%d", len(hash))
	}
	if size < 0 {
		return nil, fmt.Errorf("invalid file size, size=%d", size)
	}

	signature := make([]byte, 1+sha256.Size+8)
	signature[0] = SignatureV1Header[0]
	copy(signature[1:], hash)
	binary.BigEndian.PutUint64(signature[1+sha256.Size:], uint64(size))
	return signature, nil
}

// ValidateFileSignature verifies the current content identity encoding.
func ValidateFileSignature(signature []byte) error {
	if len(signature) != 1+sha256.Size+8 {
		return fmt.Errorf("invalid file signature length, length=%d", len(signature))
	}
	if signature[0] != SignatureV1Header[0] {
		return fmt.Errorf("invalid file signature version, version=%d", signature[0])
	}
	return nil
}

func (l *Library) MkdirAll(ctx context.Context, parentID int64, name string, perm fs.FileMode) (*File, error) {
	// Allocate the complete requested path atomically, including validation of its starting parent.
	var result *File
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = l.mkdirAll(ctx, tx, parentID, strings.TrimSpace(name), perm)
		return err
	})
	return result, err
}

func (l *Library) mkdirAll(ctx context.Context, tx *gorm.DB, parentID int64, name string, perm fs.FileMode) (*File, error) {
	// Validate the entire relative path before creating any logical directories.
	name = strings.TrimSuffix(name, "/")
	if name != "." {
		if err := entity.ValidateRelativePath(name); err != nil {
			return nil, fmt.Errorf("invalid Library directory path, %w", err)
		}
	}
	if err := validateFileParent(tx, parentID, 0); err != nil {
		return nil, err
	}

	// A current-directory request returns the existing identity without creating a literal dot node.
	current := Root
	if parentID != 0 {
		f, err := l.getFile(ctx, tx, parentID)
		if err != nil {
			return nil, err
		}
		current = f
	}
	if name == "." {
		return current, nil
	}

	// Reuse existing directories and create only missing components within the caller's transaction.
	for _, part := range strings.Split(name, "/") {
		next, err := l.mkdir(ctx, tx, current.ID, part, perm)
		if err != nil && !errors.Is(err, ErrMkdirDirExists) {
			return nil, fmt.Errorf("mkdir fail, %w", err)
		}

		current = next
	}

	return current, nil
}

func (l *Library) mkdir(ctx context.Context, tx *gorm.DB, parentID int64, name string, perm fs.FileMode) (*File, error) {
	// Existing entries are classified by logical identity, not hydrated physical facts.
	origin := new(File)
	if r := tx.Where("parent_id = ? AND name = ?", parentID, name).Find(origin); r.Error != nil {
		return nil, fmt.Errorf("mkdir: find origin fail, err= %w", r.Error)
	}
	if origin.ID != 0 {
		if origin.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
			return origin, ErrMkdirDirExists
		}
		return nil, ErrMkdirNonDirFileExists
	}

	// New logical directories always persist their kind explicitly.
	dir := &File{
		ParentID: parentID,
		Name:     name,
		Kind:     entity.FileKind_FILE_KIND_DIRECTORY,
		Mode:     uint32(fs.ModeDir | (fs.ModePerm & perm)),
		ModTime:  time.Now(),
	}
	if r := tx.Create(dir); r.Error != nil {
		return nil, fmt.Errorf("create fail, err= %w", r.Error)
	}
	return dir, nil
}

func (l *Library) GetFile(ctx context.Context, id int64) (*File, error) {
	return l.getFile(ctx, l.db.WithContext(ctx), id)
}

func (l *Library) getFile(ctx context.Context, tx *gorm.DB, id int64) (*File, error) {
	files, err := l.mGetFile(ctx, tx, id)
	if err != nil {
		return nil, err
	}

	f, ok := files[id]
	if !ok || f == nil {
		return nil, ErrFileNotFound
	}

	return f, nil
}

func (l *Library) SaveFile(ctx context.Context, file *File) error {
	return l.db.WithContext(ctx).Save(file).Error
}

func (l *Library) MoveFile(ctx context.Context, file *File) error {
	if file == nil {
		return fmt.Errorf("move requires a stored File")
	}
	return l.moveTree(ctx, file, file.Name)
}

// MoveFileToPath resolves a shared relative shortcut without pre-creating directories.
func (l *Library) MoveFileToPath(ctx context.Context, file *File, targetPath string) error {
	return l.moveTree(ctx, file, targetPath)
}

func validateFileParent(tx *gorm.DB, parentID, movingID int64) error {
	// Follow only the bounded destination ancestry; no whole-tree query or filesystem access is needed.
	seen := make(map[int64]struct{})
	for parentID != 0 {
		if parentID == movingID {
			return fmt.Errorf("cannot move a File beneath itself or its descendants")
		}
		if _, exists := seen[parentID]; exists {
			return fmt.Errorf("File destination ancestry contains a cycle")
		}
		if len(seen) >= maxFilePathDepth {
			return fmt.Errorf("File destination ancestry exceeds %d levels", maxFilePathDepth)
		}
		seen[parentID] = struct{}{}

		// Logical directory identity is authoritative; content projections are irrelevant to ancestry.
		var parent File
		if err := tx.Session(&gorm.Session{SkipHooks: true}).Select("id", "parent_id", "kind").First(&parent, parentID).Error; err != nil {
			return fmt.Errorf("read File destination parent %d failed, %w", parentID, err)
		}
		if parent.Kind != entity.FileKind_FILE_KIND_DIRECTORY {
			return fmt.Errorf("File destination parent %d is not a directory", parentID)
		}
		parentID = parent.ParentID
	}
	return nil
}

func (l *Library) Delete(ctx context.Context, ids []int64) error {
	// Retain missing identities and the reserved Trash root; common planning owns subtree retention.
	if len(ids) > 1000 {
		return fmt.Errorf("select at most 1000 operation roots")
	}
	refs := make([]string, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		_, err := l.GetFile(ctx, id)
		if errors.Is(err, ErrFileNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		refs = append(refs, strconv.FormatInt(id, 10))
	}
	if len(refs) == 0 {
		return nil
	}
	_, err := l.organizeTree(ctx, treeops.Request{Kind: treeops.Delete, Sources: refs})
	return err
}

const (
	TrashFileID = -1
)

func (l *Library) newTrash(ctx context.Context, tx *gorm.DB) (*File, error) {
	// Load the stable Trash identity so user-owned metadata survives checkpoint creation.
	now := time.Now()
	trash := new(File)
	result := tx.WithContext(ctx).Where("id = ?", TrashFileID).First(trash)
	missing := errors.Is(result.Error, gorm.ErrRecordNotFound)
	if result.Error != nil && !missing {
		return nil, fmt.Errorf("read Trash directory failed, %w", result.Error)
	}
	if missing {
		trash = &File{ID: TrashFileID}
	}

	// Refresh system-owned fields before allocating the timestamped checkpoint directory.
	trash.Name = ".Trash"
	trash.Kind = entity.FileKind_FILE_KIND_DIRECTORY
	trash.Mode = uint32(fs.ModePerm | fs.ModeDir)
	trash.ModTime = now
	if err := tx.Save(trash).Error; err != nil {
		return nil, fmt.Errorf("save Trash directory failed, %w", err)
	}
	checkpoint, err := l.mkdir(ctx, tx, trash.ID, now.Format(time.RFC3339), fs.ModePerm)
	if err != nil && !errors.Is(err, ErrMkdirDirExists) {
		return nil, fmt.Errorf("create Trash checkpoint directory failed, %w", err)
	}
	return checkpoint, nil
}

func (l *Library) MGetFile(ctx context.Context, ids ...int64) (map[int64]*File, error) {
	return l.mGetFile(ctx, l.db.WithContext(ctx), ids...)
}

func (l *Library) mGetFile(ctx context.Context, tx *gorm.DB, ids ...int64) (map[int64]*File, error) {
	if len(ids) == 0 {
		return map[int64]*File{}, nil
	}

	files := make([]*File, 0, len(ids))
	if r := tx.Where("id IN (?)", ids).Find(&files); r.Error != nil {
		return nil, fmt.Errorf("find files fail, %w", r.Error)
	}

	results := make(map[int64]*File, len(files))
	for _, f := range files {
		results[f.ID] = f
	}

	return results, nil
}

func (l *Library) GetByPath(ctx context.Context, parentID int64, name string) (*File, error) {
	name = path.Clean(strings.TrimSpace(name))
	if strings.ContainsAny(name, "\\") || name == "" {
		return nil, fmt.Errorf("unexpected mkdir path, '%s'", name)
	}

	current := Root
	if parentID != 0 {
		f, err := l.GetFile(ctx, parentID)
		if err != nil {
			return nil, err
		}
		current = f
	}

	parts := strings.Split(name, "/")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		next, err := l.GetByName(ctx, current.ID, part)
		if err != nil {
			return nil, fmt.Errorf("get by path fail, %w", err)
		}
		if next == nil {
			return nil, nil
		}

		current = next
	}

	return current, nil
}

func (l *Library) GetByName(ctx context.Context, parentID int64, name string) (*File, error) {
	return l.getByName(ctx, l.db.WithContext(ctx), parentID, name)
}

func (l *Library) getByName(ctx context.Context, tx *gorm.DB, parentID int64, name string) (*File, error) {
	file := new(File)
	if r := tx.Where("parent_id = ? AND name = ?", parentID, name).Find(file); r.Error != nil {
		return nil, fmt.Errorf("find files fail, %w", r.Error)
	}
	if file.ID == 0 {
		return nil, nil
	}
	return file, nil
}

func (l *Library) List(ctx context.Context, parentID int64) ([]*File, error) {
	return l.list(ctx, l.db.WithContext(ctx), parentID)
}

func (l *Library) ListPage(ctx context.Context, parentID int64, after string, limit int) ([]*File, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list files page limit must be positive, limit=%d", limit)
	}
	files := make([]*File, 0, limit)
	result := l.db.WithContext(ctx).
		Where("parent_id = ? AND name > ?", parentID, after).
		Order("name").Limit(limit).Find(&files)
	if result.Error != nil {
		return nil, fmt.Errorf("list files page failed, parent_id=%d, %w", parentID, result.Error)
	}
	return files, nil
}

func (l *Library) ListWithSize(ctx context.Context, parentID int64) ([]*File, error) {
	// Load the direct children that form the public result.
	files, err := l.list(ctx, l.db.WithContext(ctx), parentID)
	if err != nil {
		return nil, err
	}

	// Traverse each result directory depth-first while keeping every query page-bounded.
	type frame struct {
		directoryID int64
		after       string
	}
	for _, file := range files {
		if file.Kind != entity.FileKind_FILE_KIND_DIRECTORY {
			continue
		}

		stack := []frame{{directoryID: file.ID}}
		for len(stack) > 0 {
			last := len(stack) - 1
			current := stack[last]
			stack = stack[:last]

			page, err := l.ListPage(ctx, current.directoryID, current.after, batchSize)
			if err != nil {
				return nil, fmt.Errorf(
					"list descendant files failed, directory_id=%d after=%q, %w",
					current.directoryID,
					current.after,
					err,
				)
			}
			if len(page) == 0 {
				continue
			}

			// Sum this page and retain only its child directory identities for the DFS stack.
			directories := make([]int64, 0, len(page))
			for _, child := range page {
				file.Size += child.Size
				if child.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
					directories = append(directories, child.ID)
				}
			}

			// Resume the parent page after its child directories have been visited.
			if len(page) == batchSize {
				stack = append(stack, frame{directoryID: current.directoryID, after: page[len(page)-1].Name})
			}
			for index := len(directories) - 1; index >= 0; index-- {
				stack = append(stack, frame{directoryID: directories[index]})
			}
		}
	}
	return files, nil
}

func (l *Library) list(ctx context.Context, tx *gorm.DB, parentID int64) ([]*File, error) {
	files := make([]*File, 0, 4)
	if r := tx.Where("parent_id = ?", parentID).Order("name").Find(&files); r.Error != nil {
		return nil, fmt.Errorf("find files fail, %w", r.Error)
	}
	return files, nil
}

func (l *Library) ListParents(ctx context.Context, id int64) ([]*File, error) {
	return l.listParnets(ctx, l.db.WithContext(ctx), id)
}

func (l *Library) listParnets(ctx context.Context, tx *gorm.DB, id int64) ([]*File, error) {
	// Return a complete bounded ancestry or an error, never a truncated apparent root.
	result := make([]*File, 0, 3)
	seen := make(map[int64]struct{})
	currentID := id
	for currentID != 0 {
		if _, exists := seen[currentID]; exists {
			return nil, fmt.Errorf("File ancestry contains a cycle, file_id=%d", currentID)
		}
		if len(result) >= maxFilePathDepth {
			return nil, fmt.Errorf("File ancestry exceeds %d levels", maxFilePathDepth)
		}
		seen[currentID] = struct{}{}
		file, err := l.getFile(ctx, tx, currentID)
		if err != nil {
			return nil, err
		}

		result = append(result, file)
		currentID = file.ParentID
	}

	// Present the complete chain from the Library root toward the requested File.
	num := len(result)
	if num <= 1 {
		return result, nil
	}
	for i := 0; i < num/2; i++ {
		result[i], result[num-i-1] = result[num-i-1], result[i]
	}

	return result, nil
}

func (l *Library) Search(ctx context.Context, name string) ([]*File, error) {
	files := make([]*File, 0, 4)
	if r := l.db.WithContext(ctx).Where("name LIKE ?", "%"+name+"%").Order("name").Limit(100).Find(&files); r.Error != nil {
		return nil, fmt.Errorf("find files fail, %w", r.Error)
	}
	return files, nil
}
