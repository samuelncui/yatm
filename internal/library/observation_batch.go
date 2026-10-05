package library

import (
	"bytes"
	"fmt"
	"io/fs"

	"github.com/samuelncui/yatm/entity"
	"gorm.io/gorm"
)

// observationBatch retains only the current mutation batch's transaction-local facts.
type observationBatch struct {
	paths     map[string]*FileLocation
	originals map[int64]*FileLocation
	files     map[int64]*File
	tracking  map[int64][]*FileTrackingKey
}

func readObservationBatch(tx *gorm.DB, locationID int64, rows []*ObservedEntry) (*observationBatch, error) {
	// Path continuity and explicit identities share bounded reads before any row is written.
	paths := make([]string, 0, len(rows))
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			return nil, fmt.Errorf("missing observation")
		}
		paths = append(paths, row.Path)
		if row.FileID != 0 {
			ids = append(ids, row.FileID)
		}
	}
	local := &Library{db: tx}
	originals, err := local.ReadFileOriginalsAt(tx.Statement.Context, locationID, paths)
	if err != nil {
		return nil, err
	}
	for _, original := range originals {
		ids = append(ids, original.FileID)
	}
	ids = uniqueFileIDs(ids)
	facts, err := local.ReadFileFacts(tx.Statement.Context, ids, false, false)
	if err != nil {
		return nil, err
	}
	files, err := local.ReadFileRows(tx.Statement.Context, ids)
	if err != nil {
		return nil, err
	}
	keys, err := local.ReadFileTrackingKeys(tx.Statement.Context, ids)
	if err != nil {
		return nil, err
	}
	result := &observationBatch{paths: originals, originals: make(map[int64]*FileLocation, len(facts)), files: files, tracking: keys}
	for id, row := range facts {
		result.originals[id] = row.Original
	}
	return result, nil
}

func (b *observationBatch) admit(tx *gorm.DB, location *Location, p *ObservedEntry) (*FileLocation, error) {
	// Preserve path continuity and every existing identity/occupancy validation.
	if p == nil || !fs.FileMode(p.Mode).IsRegular() || p.Size < 0 {
		return nil, fmt.Errorf("invalid file observation")
	}
	if err := entity.ValidateRelativePath(p.Path); err != nil {
		return nil, err
	}
	previous := b.paths[p.Path]
	if previous == nil {
		previous = &FileLocation{}
	}
	if p.FileID == 0 {
		p.FileID = previous.FileID
	}
	if previous.FileID != 0 && previous.FileID != p.FileID {
		return nil, ErrLocationConflict
	}
	if p.FileID == 0 {
		file, err := createImportedFile(tx, "Unforged/"+location.Name+"/"+p.Path)
		if err != nil {
			return nil, err
		}
		p.FileID, b.files[file.ID] = file.ID, file
	}
	file := b.files[p.FileID]
	if file == nil {
		return nil, gorm.ErrRecordNotFound
	}
	if file.Kind != entity.FileKind_FILE_KIND_REGULAR {
		return nil, ErrLocationConflict
	}
	if occupied := b.originals[p.FileID]; occupied != nil && (occupied.LocationID != location.ID || occupied.Path != p.Path) {
		return nil, ErrLocationConflict
	}

	// Missing evidence retains content; contradictory native evidence invalidates it.
	unchanged := previous.Size == p.Size && previous.Mode == p.Mode && previous.MtimeNS == p.MtimeNS
	if unchanged {
		for _, old := range b.tracking[previous.FileID] {
			if old.Kind != TrackingNative {
				continue
			}
			for _, key := range p.TrackingKeys {
				if key.Kind == TrackingNative && (key.Scope != old.Scope || !bytes.Equal(key.KeyValue, old.KeyValue) || key.Details != old.Details) {
					unchanged = false
				}
			}
		}
	}
	if len(p.Signature) == 0 && len(p.Hash) == 0 && unchanged {
		p.Signature, p.Hash = previous.Signature, previous.Hash
	}

	// Learning a hash for unchanged content enriches its existing opaque identity.
	if unchanged && len(previous.Hash) == 0 && len(previous.Signature) > 0 && len(p.Hash) > 0 {
		derived, err := NewFileSignature(p.Hash, p.Size)
		if err != nil {
			return nil, err
		}
		if len(p.Signature) == 0 || bytes.Equal(p.Signature, derived) {
			p.Signature = previous.Signature
		}
	}

	// Original and tracking changes settle together in the caller's transaction.
	original := &FileLocation{FileID: p.FileID, LocationID: location.ID, Path: p.Path, Size: p.Size, Mode: p.Mode,
		MtimeNS: p.MtimeNS, Hash: p.Hash, Signature: p.Signature}
	if err := tx.Save(original).Error; err != nil {
		return nil, err
	}
	if p.TrackingKeys != nil {
		if err := replaceTrackingKeys(tx, p.FileID, location.ID, p.TrackingKeys); err != nil {
			return nil, err
		}
		b.tracking[p.FileID] = p.TrackingKeys
	}
	b.paths[p.Path], b.originals[p.FileID] = original, original
	return original, nil
}
