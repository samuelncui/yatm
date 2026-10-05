package library

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/treeops"
	"gorm.io/gorm"
)

// SignatureSize is the encoded length of the Library v1 content identity.
const SignatureSize = 1 + sha256.Size + 8

var (
	ModelFile         = new(fileRow)
	ModelFileTag      = new(FileTag)
	SignatureV1Header = []byte{0x01}

	ErrFileNotFound          = fmt.Errorf("get file: file not found")
	ErrMkdirNonDirFileExists = fmt.Errorf("mkdir: non dir exists")
	ErrMkdirDirExists        = fmt.Errorf("mkdir: dir exists")

	Root = &File{ID: 0, Kind: entity.FileKind_FILE_KIND_DIRECTORY}
)

// File is the Library-facing identity and presentation view of one logical item.
// Only identity and organization fields are persisted; Library reads attach presentation facts explicitly.
type File struct {
	ID       int64 `json:"id,omitempty"`
	ParentID int64 `json:"parent_id,omitempty"`

	Name        string          `json:"name,omitempty"`
	Kind        entity.FileKind `json:"kind"`
	CreatedAtNS int64           `json:"created_at_ns,string"`
	UpdatedAtNS int64           `json:"updated_at_ns,string"`
	// Presentation facts are derived from the original or a saved version, never persisted on File.
	Mode    uint32    `json:"-"`
	ModTime time.Time `json:"-"`
	Hash    []byte    `json:"-"`
	Size    int64     `json:"-"`
	Note    string    `json:"note,omitempty"`
	Tags    []string  `json:"tags,omitempty"`

	Signature      []byte                     `json:"-"`
	ContentSummary *entity.FileContentSummary `json:"-"`
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

	signature := make([]byte, SignatureSize)
	signature[0] = SignatureV1Header[0]
	copy(signature[1:], hash)
	binary.BigEndian.PutUint64(signature[1+sha256.Size:], uint64(size))
	return signature, nil
}

// ValidateFileSignature verifies the current content identity encoding.
func ValidateFileSignature(signature []byte) error {
	if len(signature) != SignatureSize {
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
		result, err = l.mkdirAll(ctx, tx, parentID, name, perm)
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
	row := new(fileRow)
	if r := tx.Where("parent_id = ? AND name = ?", parentID, name).Find(row); r.Error != nil {
		return nil, fmt.Errorf("mkdir: find origin fail, err= %w", r.Error)
	}
	origin := row.file()
	if origin.ID != 0 {
		if origin.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
			if err := hydrateFileViews(tx, origin); err != nil {
				return nil, fmt.Errorf("hydrate existing directory failed, %w", err)
			}
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
	if err := createFileRow(tx, dir); err != nil {
		return nil, fmt.Errorf("create fail, err= %w", err)
	}
	return dir, nil
}

func (l *Library) GetFile(ctx context.Context, id int64) (*File, error) {
	return l.getFile(ctx, l.readDB().WithContext(ctx), id)
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
	// A nil caller value cannot be converted into the persisted File identity.
	if file == nil {
		return fmt.Errorf("save File requires a value")
	}

	// The persistence boundary validates the literal name for every writer.
	return saveFileRow(l.db.WithContext(ctx), file)
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
		var parent fileRow
		if err := tx.Select("id", "parent_id", "kind").First(&parent, parentID).Error; err != nil {
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
	row := new(fileRow)
	result := tx.WithContext(ctx).Where("id = ?", TrashFileID).First(row)
	missing := errors.Is(result.Error, gorm.ErrRecordNotFound)
	if result.Error != nil && !missing {
		return nil, fmt.Errorf("read Trash directory failed, %w", result.Error)
	}
	if missing {
		row = &fileRow{ID: TrashFileID}
	}
	trash := row.file()

	// Refresh system-owned fields before allocating the timestamped checkpoint directory.
	trash.Name = ".Trash"
	trash.Kind = entity.FileKind_FILE_KIND_DIRECTORY
	trash.Mode = uint32(fs.ModePerm | fs.ModeDir)
	trash.ModTime = now
	if err := saveFileRow(tx, trash); err != nil {
		return nil, fmt.Errorf("save Trash directory failed, %w", err)
	}

	// Reuse this timestamp's directory, suffixing only a conflicting non-directory name.
	base := now.Format(time.RFC3339)
	for suffix := 1; ; suffix++ {
		name := base
		if suffix > 1 {
			name = fmt.Sprintf("%s (%d)", base, suffix)
		}
		checkpoint, err := l.mkdir(ctx, tx, trash.ID, name, fs.ModePerm)
		if errors.Is(err, ErrMkdirNonDirFileExists) {
			continue
		}
		if err != nil && !errors.Is(err, ErrMkdirDirExists) {
			return nil, fmt.Errorf("create Trash checkpoint directory failed, %w", err)
		}
		return checkpoint, nil
	}
}

func (l *Library) MGetFile(ctx context.Context, ids ...int64) (map[int64]*File, error) {
	return l.mGetFile(ctx, l.readDB().WithContext(ctx), ids...)
}

func (l *Library) mGetFile(ctx context.Context, tx *gorm.DB, ids ...int64) (map[int64]*File, error) {
	// Keep the persistence read independent from the compatibility projection.
	files, err := readFileRows(tx, ids...)
	if err != nil {
		return nil, err
	}
	views := make([]*File, 0, len(files))
	for _, file := range files {
		views = append(views, file)
	}
	if err := hydrateFileViews(tx, views...); err != nil {
		return nil, fmt.Errorf("hydrate files fail, %w", err)
	}
	return files, nil
}

func readFileRows(tx *gorm.DB, ids ...int64) (map[int64]*File, error) {
	if len(ids) == 0 {
		return map[int64]*File{}, nil
	}

	// Materialize only the persisted shape before converting it to caller-facing values.
	rows := make([]*fileRow, 0, len(ids))
	if r := tx.Where("id IN (?)", ids).Find(&rows); r.Error != nil {
		return nil, fmt.Errorf("find files fail, %w", r.Error)
	}
	files := fileViews(rows)

	results := make(map[int64]*File, len(files))
	for _, f := range files {
		results[f.ID] = f
	}

	return results, nil
}

func (l *Library) GetByPath(ctx context.Context, parentID int64, name string) (*File, error) {
	// Resolve slash shortcuts within the selected root while preserving every literal component.
	var parts []string
	for _, part := range strings.Split(name, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) == 0 {
				return nil, fmt.Errorf("Library path escapes the selected root, path=%q", name)
			}
			parts = parts[:len(parts)-1]
		default:
			if err := entity.ValidatePathComponent(part); err != nil {
				return nil, fmt.Errorf("invalid Library path, %w", err)
			}
			parts = append(parts, part)
		}
	}

	// The starting directory is the caller's explicit Library root, never a physical working directory.
	current := Root
	if parentID != 0 {
		f, err := l.GetFile(ctx, parentID)
		if err != nil {
			return nil, err
		}
		current = f
	}

	// Exact parent/name lookup preserves whitespace and literal backslashes at every depth.
	for _, part := range parts {
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
	return l.getByName(ctx, l.readDB().WithContext(ctx), parentID, name)
}

func (l *Library) getByName(ctx context.Context, tx *gorm.DB, parentID int64, name string) (*File, error) {
	// Resolve the persisted identity before attaching its compatibility projection.
	row := new(fileRow)
	if r := tx.Where("parent_id = ? AND name = ?", parentID, name).Find(row); r.Error != nil {
		return nil, fmt.Errorf("find files fail, %w", r.Error)
	}
	file := row.file()
	if file.ID == 0 {
		return nil, nil
	}
	if err := hydrateFileViews(tx, file); err != nil {
		return nil, fmt.Errorf("hydrate file fail, %w", err)
	}
	return file, nil
}

func (l *Library) List(ctx context.Context, parentID int64) ([]*File, error) {
	return l.list(ctx, l.readDB().WithContext(ctx), parentID)
}

func (l *Library) ListPage(ctx context.Context, parentID int64, after string, limit int) ([]*File, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list files page limit must be positive, limit=%d", limit)
	}

	// Read the bounded persisted page before attaching compatibility projections.
	rows := make([]*fileRow, 0, limit)
	result := l.readDB().WithContext(ctx).
		Where("parent_id = ? AND name > ?", parentID, after).
		Order("name").Limit(limit).Find(&rows)
	if result.Error != nil {
		return nil, fmt.Errorf("list files page failed, parent_id=%d, %w", parentID, result.Error)
	}
	files := fileViews(rows)
	if err := hydrateFileViews(l.readDB().WithContext(ctx), files...); err != nil {
		return nil, fmt.Errorf("hydrate files page failed, parent_id=%d, %w", parentID, err)
	}
	return files, nil
}

func (l *Library) list(ctx context.Context, tx *gorm.DB, parentID int64) ([]*File, error) {
	// Read the complete persisted directory before attaching compatibility projections.
	rows := make([]*fileRow, 0, 4)
	if r := tx.Where("parent_id = ?", parentID).Order("name").Find(&rows); r.Error != nil {
		return nil, fmt.Errorf("find files fail, %w", r.Error)
	}
	files := fileViews(rows)
	if err := hydrateFileViews(tx, files...); err != nil {
		return nil, fmt.Errorf("hydrate files fail, %w", err)
	}
	return files, nil
}

func (l *Library) ListParents(ctx context.Context, id int64) ([]*File, error) {
	return l.listParents(l.readDB().WithContext(ctx), id)
}

func (l *Library) listParents(tx *gorm.DB, id int64) ([]*File, error) {
	result, err := listFileRowParents(tx, id)
	if err != nil {
		return nil, err
	}
	if err := hydrateFileViews(tx, result...); err != nil {
		return nil, fmt.Errorf("hydrate File ancestry failed, %w", err)
	}
	return result, nil
}

func listFileRowParents(tx *gorm.DB, id int64) ([]*File, error) {
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
		files, err := readFileRows(tx, currentID)
		if err != nil {
			return nil, err
		}
		file := files[currentID]
		if file == nil {
			return nil, ErrFileNotFound
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
	// Search persisted identities before attaching compatibility projections.
	rows := make([]*fileRow, 0, 4)
	if r := l.readDB().WithContext(ctx).Where("name LIKE ?", "%"+name+"%").Order("name").Limit(100).Find(&rows); r.Error != nil {
		return nil, fmt.Errorf("find files fail, %w", r.Error)
	}
	files := fileViews(rows)
	if err := hydrateFileViews(l.readDB().WithContext(ctx), files...); err != nil {
		return nil, fmt.Errorf("hydrate files fail, %w", err)
	}
	return files, nil
}
