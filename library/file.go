package library

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var (
	ModelFile         = new(File)
	SignatureV1Header = []byte{0x01}

	ErrFileNotFound          = fmt.Errorf("get file: file not found")
	ErrMkdirNonDirFileExists = fmt.Errorf("mkdir: non dir exists")
	ErrMkdirDirExists        = fmt.Errorf("mkdir: dir exists")
	ErrNewFileFileExists     = fmt.Errorf("new file: file exists")

	Root = &File{ID: 0}
)

type File struct {
	ID       int64 `gorm:"primaryKey;autoIncrement" json:"id,omitempty"`
	ParentID int64 `gorm:"index:idx_parent_name,unique" json:"parent_id,omitempty"`

	Name    string    `gorm:"type:varchar(256);index:idx_parent_name,unique" json:"name,omitempty"`
	Mode    uint32    `json:"mode,omitempty"`
	ModTime time.Time `json:"mod_time,omitempty"`
	Hash    []byte    `gorm:"type:varbinary(32)" json:"hash,omitempty"` // sha256
	Size    int64     `json:"size,omitempty"`

	Signature []byte `gorm:"type:varbinary(256);index:idx_signature" json:"signature,omitempty"` // sha256 + size
}

func (l *Library) MkdirAll(ctx context.Context, parentID int64, name string, perm fs.FileMode) (*File, error) {
	return l.mkdirAll(ctx, l.db.WithContext(ctx), parentID, name, perm)
}

func (l *Library) mkdirAll(ctx context.Context, tx *gorm.DB, parentID int64, name string, perm fs.FileMode) (*File, error) {
	name = path.Clean(strings.TrimSpace(name))
	if strings.ContainsAny(name, "\\") || name == "" {
		return nil, fmt.Errorf("unexpected mkdir path, '%s'", name)
	}

	current := Root
	if parentID != 0 {
		f, err := l.getFile(ctx, tx, parentID)
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

		next, err := l.mkdir(ctx, tx, current.ID, part, perm)
		if err != nil && !errors.Is(err, ErrMkdirDirExists) {
			return nil, fmt.Errorf("mkdir fail, %w", err)
		}

		current = next
	}

	return current, nil
}

func (l *Library) mkdir(ctx context.Context, tx *gorm.DB, parentID int64, name string, perm fs.FileMode) (*File, error) {
	perm = fs.ModePerm & perm

	origin := new(File)
	if r := tx.Where("parent_id = ? AND name = ?", parentID, name).Find(origin); r.Error != nil {
		return nil, fmt.Errorf("mkdir: find origin fail, err= %w", r.Error)
	}
	if origin.ID != 0 {
		if fs.FileMode(origin.Mode).IsDir() {
			return origin, ErrMkdirDirExists
		}
		return nil, ErrMkdirNonDirFileExists
	}

	dir := &File{
		ParentID: parentID,
		Name:     name,
		Mode:     uint32(fs.ModeDir | perm),
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
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return l.moveFile(ctx, tx, file)
	})
}

func (l *Library) moveFile(ctx context.Context, tx *gorm.DB, file *File) error {
	origin, err := l.getByName(ctx, tx, file.ParentID, file.Name)
	if err != nil {
		return err
	}
	if origin == nil {
		return tx.Save(file).Error
	}

	if !fs.FileMode(origin.Mode).IsDir() {
		return fmt.Errorf("same name file exists, name= '%s'", file.Name)
	}
	if !fs.FileMode(file.Mode).IsDir() {
		return fmt.Errorf("same name file is a dir, name= '%s", file.Name)
	}

	children, err := l.list(ctx, tx, file.ID)
	if err != nil {
		return err
	}
	for _, child := range children {
		child.ParentID = origin.ID
		if err := l.moveFile(ctx, tx, child); err != nil {
			return err
		}
	}

	if file.ModTime.After(origin.ModTime) {
		origin.ModTime = file.ModTime
		if err := tx.Save(origin).Error; err != nil {
			return err
		}
	}
	if err := tx.Delete(file).Error; err != nil {
		return err
	}

	return nil
}

func (l *Library) Delete(ctx context.Context, ids []int64) error {
	files, err := l.MGetFile(ctx, ids...)
	if err != nil {
		panic(err)
	}

	moveToTrash := make([]*File, 0, len(files))
outter:
	for _, file := range files {
		if file.ID == TrashFileID {
			continue
		}
		parents, err := l.ListParents(ctx, file.ID)
		if err != nil {
			panic(err)
		}
		if len(parents) == 0 {
			moveToTrash = append(moveToTrash, file)
			continue
		}
		if parents[0].ID != TrashFileID {
			moveToTrash = append(moveToTrash, file)
			continue
		}
		if !fs.FileMode(file.Mode).IsDir() {
			continue
		}

		needDelete := make([]*File, 0, 8)
		current := []*File{file}
		for len(current) > 0 {
			next := make([]*File, 0, 8)
			for _, file := range current {
				children, err := l.List(ctx, file.ID)
				if err != nil {
					return err
				}
				for _, child := range children {
					if !fs.FileMode(child.Mode).IsDir() {
						continue outter
					}
				}
				next = append(next, children...)
			}

			needDelete = append(needDelete, current...)
			current = next
		}

		if err := l.db.WithContext(ctx).Delete(needDelete).Error; err != nil {
			return err
		}
	}
	if len(moveToTrash) == 0 {
		return nil
	}

	trash, err := l.newTrash(ctx, l.db.WithContext(ctx))
	if err != nil {
		return err
	}

	for _, file := range moveToTrash {
		file.ParentID = trash.ID
		if err := l.MoveFile(ctx, file); err != nil {
			return err
		}
	}

	return nil
}

const (
	TrashFileID = -1
)

func (l *Library) newTrash(ctx context.Context, tx *gorm.DB) (*File, error) {
	now := time.Now()
	trash := &File{
		ID:      TrashFileID,
		Name:    ".Trash",
		Mode:    uint32(fs.ModePerm | fs.ModeDir),
		ModTime: now,
	}
	if err := tx.Save(trash).Error; err != nil {
		return nil, err
	}

	return l.mkdir(ctx, tx, trash.ID, now.Format(time.RFC3339), fs.ModePerm)
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
	result := make([]*File, 0, 3)

	currentID := id
	for i := 0; i < 32 && currentID != 0; i++ {
		file, err := l.getFile(ctx, tx, currentID)
		if err != nil {
			return nil, err
		}

		result = append(result, file)
		currentID = file.ParentID
	}

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
	if r := l.db.WithContext(ctx).Where("name LIKE ?", fmt.Sprintf("%"+name+"%")).Order("name").Limit(100).Find(&files); r.Error != nil {
		return nil, fmt.Errorf("find files fail, %w", r.Error)
	}
	return files, nil
}

func (l *Library) TrimFiles(ctx context.Context) error {
	for {
		positions := make([]*Position, 0, batchSize)
		if r := l.db.WithContext(ctx).Where("file_id = ?", 0).Limit(batchSize).Find(&positions); r.Error != nil {
			return fmt.Errorf("list non file position fail, err= %w", r.Error)
		}
		if len(positions) == 0 {
			return nil
		}

		signatures := make([][]byte, 0, len(positions))
		sign2positions := make(map[string]*Position, len(positions))
		for _, posi := range positions {
			size := make([]byte, 8)
			binary.BigEndian.PutUint64(size, uint64(posi.Size))

			sign := make([]byte, 0, 64)
			sign = append(sign, SignatureV1Header...)
			sign = append(sign, posi.Hash...)
			sign = append(sign, size...)

			signatures = append(signatures, sign)
			sign2positions[string(sign)] = posi
		}

		matched := make([]*File, 0, 4)
		if r := l.db.WithContext(ctx).Where("signature IN (?)", signatures).Find(&matched); r.Error != nil {
			return fmt.Errorf("get matched file fail, err= %w", r.Error)
		}

		for _, file := range matched {
			posi, has := sign2positions[string(file.Signature)]
			if !has {
				continue
			}

			posi.FileID = file.ID
			l.db.WithContext(ctx).Save(posi)

			delete(sign2positions, string(file.Signature))
		}

		tapeIDs := mapset.NewThreadUnsafeSet[int64]()
		for _, posi := range sign2positions {
			tapeIDs.Add(posi.TapeID)
		}

		tapes, err := l.MGetTape(ctx, tapeIDs.ToSlice()...)
		if err != nil {
			return fmt.Errorf("mget tape, ids= %v, %w", tapeIDs.ToSlice(), err)
		}

		for sign, posi := range sign2positions {
			tape := tapes[posi.TapeID]
			if tape == nil {
				logrus.WithContext(ctx).Warnf("trim file, tape not found, tape_id= %d", posi.TapeID)
				continue
			}

			dirname, filename := path.Split(fmt.Sprintf("Unforged/%s/%s", tape.Barcode, posi.Path))
			dir, err := l.MkdirAll(ctx, Root.ID, dirname, 0x777)
			if err != nil {
				return fmt.Errorf("mkdir, %w", err)
			}

			origin := new(File)
			if r := l.db.WithContext(ctx).Where("parent_id = ? AND name = ?", dir.ID, filename).Find(origin); r.Error != nil {
				return fmt.Errorf("new file: find origin fail, err= %w", r.Error)
			}
			if origin.ID != 0 {
				return ErrNewFileFileExists
			}

			file := &File{
				ParentID:  dir.ID,
				Name:      filename,
				Mode:      posi.Mode,
				ModTime:   time.Now(),
				Hash:      posi.Hash,
				Size:      posi.Size,
				Signature: []byte(sign),
			}
			if r := l.db.WithContext(ctx).Create(file); r.Error != nil {
				return fmt.Errorf("new file: create fail, err= %w", r.Error)
			}
		}
	}
}
