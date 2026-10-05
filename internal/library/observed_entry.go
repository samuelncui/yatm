package library

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// ObservedEntry is what one read saw for one entry of a Location, whether or not a File matched it.
// It is the input a Scan publishes and the before/after facts of a Scan result, not a stored
// entity: a recorded entry is a FileLocation, and a listed one is an entity.LocationEntry.
type ObservedEntry struct {
	Path         string
	IsDir        bool
	FileID       int64
	Size         int64
	Mode         uint32
	MtimeNS      int64
	Hash         []byte
	Signature    []byte
	TrackingKeys []*FileTrackingKey
}

// Observation states a recorded association as the observation it was published from.
func (p *FileLocation) Observation() *entity.ObservedEntry {
	return &entity.ObservedEntry{Path: p.Path, FileId: p.FileID, SizeBytes: p.Size, Mode: p.Mode,
		MtimeNs: p.MtimeNS, Sha256: p.Hash, Signature: p.Signature}
}

// ObservationManifest supplies a complete validated observation in strict physical path order.
type ObservationManifest func(context.Context, func(*ObservedEntry) error) error

func (l *Library) bindObservedFile(ctx context.Context, tx *gorm.DB, location *Location, p *ObservedEntry) (*File, error) {
	// A matched File may change content, but cannot acquire a second original.
	if p.FileID > 0 {
		var row fileRow
		if err := tx.First(&row, p.FileID).Error; err != nil {
			return nil, err
		}
		if row.Kind != entity.FileKind_FILE_KIND_REGULAR {
			return nil, fmt.Errorf("original cannot bind a directory")
		}
		var count int64
		if err := tx.Model(&FileLocation{}).Where("file_id = ?", p.FileID).Count(&count).Error; err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, fmt.Errorf("File %d already has an original", p.FileID)
		}
		return row.file(), nil
	}

	// New leaves receive collision suffixes; prior organization and annotations never move.
	return createImportedFile(tx, "Unforged/"+location.Name+"/"+p.Path)
}

func createImportedFile(tx *gorm.DB, logicalPath string) (*File, error) {
	// Create only missing directory nodes and a new independently organized leaf.
	parentID := int64(0)
	parts := strings.Split(logicalPath, "/")
	for _, name := range parts[:len(parts)-1] {
		directory, err := importedFileNode(tx, parentID, name, true)
		if err != nil {
			return nil, err
		}
		parentID = directory.ID
	}
	file, err := importedFileNode(tx, parentID, parts[len(parts)-1], false)
	if err != nil {
		return nil, err
	}
	if err := createFileRow(tx, file); err != nil {
		return nil, err
	}
	return file, nil
}

func importedFileNode(tx *gorm.DB, parentID int64, name string, directory bool) (*File, error) {
	// Reject invalid names before resolving or creating any logical node.
	if !utf8.ValidString(name) {
		return nil, fmt.Errorf("imported original component is not valid UTF-8")
	}

	// Reuse a directory only; every colliding leaf receives a new deterministic name.
	for suffix := 0; ; suffix++ {
		candidate := importedFileName(name, directory, suffix)
		var stored fileRow
		if err := tx.Where("parent_id = ? AND name = ?", parentID, candidate).Find(&stored).Error; err != nil {
			return nil, err
		}
		if stored.ID != 0 {
			if directory && stored.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
				return stored.file(), nil
			}
			continue
		}
		file := &File{ParentID: parentID, Name: candidate}
		if !directory {
			return file, nil
		}
		file.Kind = entity.FileKind_FILE_KIND_DIRECTORY
		file.Mode, file.ModTime = uint32(fs.ModeDir|0755), time.Now()
		if err := createFileRow(tx, file); err != nil {
			return nil, err
		}
		return file, nil
	}
}

// importedFileName keeps creation and existing-entry lookup on the same bounded names.
func importedFileName(name string, directory bool, suffix int) string {
	if suffix == 0 {
		return importedNamePrefix(name, 256)
	}
	extension := ""
	if !directory {
		extension = path.Ext(name)
	}
	marker := fmt.Sprintf(" (%d)", suffix)
	ext := importedNamePrefix(extension, 256-len(marker))
	return importedNamePrefix(strings.TrimSuffix(name, extension), 256-len(marker)-len(ext)) + marker + ext
}

func importedNamePrefix(value string, limit int) string {
	// Truncate only a newly imported name, at a UTF-8 boundary, to leave room for its suffix.
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}
