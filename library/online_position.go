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

// OnlinePosition is a bounded observation/browse DTO, not a persisted ownership table.
type OnlinePosition struct {
	ID                   int64
	SourceID             int64
	Path                 string
	ParentPath           string
	IsDir                bool
	FileID               int64
	Size                 int64
	Mode                 uint32
	MtimeNS              int64
	Hash                 []byte
	Signature            []byte
	TrackingKeys         []*FileTrackingKey
	CopyResult           *FileOperationResult
	Independent          bool
	ObservedBindingToken string
}

func originalObservation(p *FileLocation) *OnlinePosition {
	return &OnlinePosition{ID: p.FileID, FileID: p.FileID, SourceID: p.LocationID, Path: p.Path,
		ParentPath: p.ParentPath, Size: p.Size, Mode: p.Mode, MtimeNS: p.MtimeNS, Hash: p.Hash, Signature: p.Signature,
		ObservedBindingToken: p.ObservedBindingToken}
}

func (p *OnlinePosition) ToEntity() *entity.OnlinePosition {
	return &entity.OnlinePosition{Id: p.ID, SourceId: p.SourceID, Path: p.Path, ParentPath: p.ParentPath,
		IsDir: p.IsDir, FileId: p.FileID, Size: p.Size, Mode: p.Mode, MtimeNs: p.MtimeNS, Sha256: p.Hash, Signature: p.Signature,
		ObservedBindingToken: p.ObservedBindingToken}
}

func (l *Library) GetOnlinePosition(ctx context.Context, id int64) (*OnlinePosition, error) {
	p, err := l.GetFileLocation(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return originalObservation(p), nil
}

// ListOnlinePositions groups recorded references for Restore ownership checks, not filesystem browsing.
func (l *Library) ListOnlinePositions(ctx context.Context, locationID int64, parent, after string, revision int64, limit int) ([]*OnlinePosition, int64, bool, error) {
	// This internal query derives ancestor names from references without persisting directory inventory.
	if limit <= 0 || limit > 1000 {
		return nil, 0, false, fmt.Errorf("invalid Location page size")
	}
	if after != "" && revision == 0 {
		return nil, 0, false, ErrOnlineConflict
	}
	var rows []*OnlinePosition
	var current int64
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var location Location
		if err := tx.First(&location, locationID).Error; err != nil {
			return err
		}
		if revision != 0 && location.Revision != revision {
			return ErrOnlineConflict
		}
		current = location.Revision
		cursor := parent
		previous := ""
		for {
			var originals []*FileLocation
			if err := tx.Where("location_id = ? AND path > ?", locationID, cursor).
				Order("path").Limit(batchSize).Find(&originals).Error; err != nil {
				return err
			}
			if len(originals) == 0 {
				return nil
			}
			for _, p := range originals {
				if !strings.HasPrefix(p.Path, parent) {
					return nil
				}
				child := originalObservation(p)
				if offset := strings.Index(strings.TrimPrefix(p.Path, parent), "/"); offset >= 0 {
					name := p.Path[:len(parent)+offset+1]
					child = &OnlinePosition{SourceID: locationID, Path: name, ParentPath: parent, IsDir: true}
				}
				if child.Path <= after || child.Path == previous {
					continue
				}
				previous = child.Path
				rows = append(rows, child)
				if len(rows) > limit {
					return nil
				}
			}
			cursor = originals[len(originals)-1].Path
		}
	})

	// The source revision protects continuations from replacement indexes.
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	return rows, current, more, err
}

func (l *Library) ListFileOnlinePositions(ctx context.Context, fileID, after int64, limit int) ([]*OnlinePosition, bool, error) {
	if fileID <= 0 || limit <= 0 || limit > 1000 {
		return nil, false, fmt.Errorf("invalid File original query")
	}
	if after >= fileID {
		return nil, false, nil
	}
	value, err := l.GetFileLocation(ctx, fileID)
	if err != nil || value == nil {
		return nil, false, err
	}
	return []*OnlinePosition{originalObservation(value)}, false, nil
}

// OnlineFilesPage reads only ordinary originals, regardless of current accessibility.
func (l *Library) OnlineFilesPage(ctx context.Context, locationID int64, after string, limit int) ([]*OnlinePosition, error) {
	if limit <= 0 || limit > 1000 {
		return nil, fmt.Errorf("invalid original manifest page size")
	}
	var values []*FileLocation
	if err := l.db.WithContext(ctx).Where("location_id = ? AND path > ?", locationID, after).
		Order("path").Limit(limit).Find(&values).Error; err != nil {
		return nil, err
	}
	rows := make([]*OnlinePosition, 0, len(values))
	if len(values) == 0 {
		return rows, nil
	}
	// Native lifetime evidence guards content reuse without one query per original.
	ids := make([]int64, 0, len(values))
	for _, value := range values {
		ids = append(ids, value.FileID)
	}
	var keys []*FileTrackingKey
	if err := l.db.WithContext(ctx).Where("file_id IN ?", ids).Find(&keys).Error; err != nil {
		return nil, err
	}
	byFile := make(map[int64][]*FileTrackingKey, len(values))
	for _, key := range keys {
		byFile[key.FileID] = append(byFile[key.FileID], key)
	}
	for _, p := range values {
		observation := originalObservation(p)
		observation.TrackingKeys = byFile[p.FileID]
		rows = append(rows, observation)
	}
	return rows, nil
}

// OnlineManifest supplies a complete validated observation in strict physical path order.
type OnlineManifest func(context.Context, func(*OnlinePosition) error) error

func (l *Library) PublishOnline(ctx context.Context, locationID, revision, jobID int64, manifest OnlineManifest) (*Location, error) {
	// The caller holds the Location operation lock through filesystem validation and publication.
	if manifest == nil {
		return nil, fmt.Errorf("original manifest is missing")
	}
	var location Location
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Reject stale configuration before replacing any last-successful facts.
		if err := tx.First(&location, locationID).Error; err != nil {
			return err
		}
		if location.Revision != revision {
			return ErrOnlineConflict
		}
		if location.Binding != entity.OnlineBinding_CONFIRMED {
			return ErrOnlineUnverified
		}
		if err := tx.Where("location_id = ?", locationID).Delete(&FileLocation{}).Error; err != nil {
			return err
		}

		// Store each resolved File independently; equality is coverage, never automatic identity merging.
		previous := ""
		files := func(ctx context.Context, yield func(*Position) error) error {
			return manifest(ctx, func(p *OnlinePosition) error {
				if p == nil || p.IsDir || !fs.FileMode(p.Mode).IsRegular() || p.Size < 0 {
					return fmt.Errorf("invalid original file facts")
				}
				if len(p.Hash) != 0 && len(p.Hash) != 32 {
					return fmt.Errorf("invalid original SHA-256")
				}
				if err := entity.ValidateRelativePath(p.Path); err != nil {
					return err
				}
				if p.Path <= previous || OnlineExcluded(location.Exclusions, p.Path) {
					return fmt.Errorf("invalid original path %q", p.Path)
				}
				previous = p.Path
				if len(p.Signature) == 0 && len(p.Hash) > 0 {
					value, err := NewFileSignature(p.Hash, p.Size)
					if err != nil {
						return err
					}
					p.Signature = value
				}
				file, err := l.bindOnlineFile(ctx, tx, &location, p)
				if err != nil {
					return err
				}
				p.FileID = file.ID
				if err := tx.Where("file_id = ?", file.ID).Delete(&FileTrackingKey{}).Error; err != nil {
					return err
				}
				for _, key := range p.TrackingKeys {
					key.FileID, key.LocationID, key.ObservedAt = file.ID, locationID, time.Now().UnixMilli()
					if err := tx.Create(key).Error; err != nil {
						return err
					}
				}
				if err := tx.Create(&FileLocation{FileID: file.ID, LocationID: locationID, Path: p.Path, ObservedBindingToken: location.BindingToken,
					Size: p.Size, Mode: p.Mode, MtimeNS: p.MtimeNS, Signature: nullableSignature(p.Signature), Hash: p.Hash}).Error; err != nil {
					return err
				}
				return yield(&Position{Path: p.Path, Size: p.Size, Mode: p.Mode,
					Hash: p.Hash, Signature: p.Signature, ModTime: time.Unix(0, p.MtimeNS)})
			})
		}

		// Compatibility fixtures publish ordinary associations, never a physical directory cache.
		if err := files(ctx, func(*Position) error { return nil }); err != nil {
			return err
		}

		// Retain covered content before later Sync attempts can replace the original observation.
		if err := reconcileCoveredVersions(tx, tx.Where("location_id = ?", locationID)); err != nil {
			return err
		}

		// Successful publication changes the revision and timestamps with the entire index.
		location.Binding = entity.OnlineBinding_CONFIRMED
		location.LastSyncAt, location.LastSyncJobID = time.Now().UnixMilli(), jobID
		return tx.Save(&location).Error
	})
	if err != nil {
		return nil, fmt.Errorf("publish Location failed, %w", err)
	}
	return &location, nil
}

func (l *Library) bindOnlineFile(ctx context.Context, tx *gorm.DB, location *Location, p *OnlinePosition) (*File, error) {
	// A matched File may change content, but cannot acquire a second online original.
	if p.FileID > 0 {
		var file File
		if err := tx.First(&file, p.FileID).Error; err != nil {
			return nil, err
		}
		if file.Kind != entity.FileKind_FILE_KIND_REGULAR {
			return nil, fmt.Errorf("original cannot bind a directory")
		}
		var count int64
		if err := tx.Model(&FileLocation{}).Where("file_id = ?", p.FileID).Count(&count).Error; err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, fmt.Errorf("File %d already has an original", p.FileID)
		}
		return &file, nil
	}

	// New leaves receive collision suffixes; prior organization and annotations never move.
	return createImportedFile(tx, "Unforged/"+location.Name+"/"+p.Path)
}

func createImportedFile(tx *gorm.DB, logicalPath string) (*File, error) {
	// Create only missing directory nodes and a new independently organized leaf.
	parentID := int64(0)
	parts := strings.Split(logicalPath, "/")
	for _, name := range parts[:len(parts)-1] {
		directory, err := onlineImportNode(tx, parentID, name, true)
		if err != nil {
			return nil, err
		}
		parentID = directory.ID
	}
	file, err := onlineImportNode(tx, parentID, parts[len(parts)-1], false)
	if err != nil {
		return nil, err
	}
	if err := tx.Create(file).Error; err != nil {
		return nil, err
	}
	return file, nil
}

func onlineImportNode(tx *gorm.DB, parentID int64, name string, directory bool) (*File, error) {
	// Keep suffixes within the Library component bound, including multibyte source names.
	if !utf8.ValidString(name) {
		return nil, fmt.Errorf("online import component is not valid UTF-8")
	}
	extension := path.Ext(name)
	if directory {
		extension = ""
	}
	base := strings.TrimSuffix(name, extension)

	// Reuse a directory only; every colliding leaf receives a new deterministic name.
	for suffix := 0; ; suffix++ {
		candidate := name
		if suffix > 0 {
			marker := fmt.Sprintf(" (%d)", suffix)
			ext := onlineNamePrefix(extension, 256-len(marker))
			candidate = onlineNamePrefix(base, 256-len(marker)-len(ext)) + marker + ext
		} else {
			candidate = onlineNamePrefix(candidate, 256)
		}
		var stored File
		if err := tx.Where("parent_id = ? AND name = ?", parentID, candidate).Find(&stored).Error; err != nil {
			return nil, err
		}
		if stored.ID != 0 {
			if directory && stored.Kind == entity.FileKind_FILE_KIND_DIRECTORY {
				return &stored, nil
			}
			continue
		}
		file := &File{ParentID: parentID, Name: candidate}
		if !directory {
			return file, nil
		}
		file.Kind = entity.FileKind_FILE_KIND_DIRECTORY
		file.Mode, file.ModTime = uint32(fs.ModeDir|0755), time.Now()
		if err := tx.Create(file).Error; err != nil {
			return nil, err
		}
		return file, nil
	}
}

func onlineNamePrefix(value string, limit int) string {
	// Truncate only a newly imported name, at a UTF-8 boundary, to leave room for its suffix.
	if len(value) <= limit {
		return value
	}
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}
