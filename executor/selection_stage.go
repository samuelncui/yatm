package executor

import (
	"context"
	"fmt"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor/observation"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type selectionDirectory struct {
	Path      string                   `gorm:"primaryKey"`
	Reference *entity.LocationEntryRef `gorm:"serializer:json;type:blob;not null"`
}

func (selectionDirectory) TableName() string { return "selection_directories" }

// InitSelectionObservations initializes bounded preparation tables in an Archive or Preview bundle.
func InitSelectionObservations(db *gorm.DB) error {
	return db.AutoMigrate(&observation.Item{}, &observation.Original{}, &selectionDirectory{})
}

func (e *Executor) stageLiveSelections(ctx context.Context, db *gorm.DB, locationID int64, roots []*entity.LocationSelection) error {
	release, err := e.lib.UseOnlineSource(locationID)
	if err != nil {
		return err
	}
	defer release()
	location, err := e.lib.GetOnlineSource(ctx, locationID)
	if err != nil {
		return err
	}
	// These are reusable preparation rows, not a second Library or a recovery journal.
	for _, model := range []any{&observation.Item{}, &observation.Original{}, &selectionDirectory{}} {
		if err := db.WithContext(ctx).Where("1 = 1").Delete(model).Error; err != nil {
			return err
		}
	}
	for _, root := range roots {
		entry, err := e.ObserveLocationEntry(ctx, locationID, root.Path)
		if err != nil {
			return err
		}
		ref := entry.Reference
		if root.Reference != nil {
			if root.Reference.LocationId != locationID || root.Reference.Path != root.Path || root.Reference.BindingToken != ref.BindingToken {
				return library.ErrOnlineConflict
			}
			if root.Reference.Facts != nil {
				ref = root.Reference
			}
		}
		err = e.walkLiveSelectionScope(ctx, location, ref, false, 0,
			func(ref *entity.LocationEntryRef) error { return e.stageSelectedFile(ctx, db, location, ref) },
			func(ref *entity.LocationEntryRef) error {
				return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&selectionDirectory{Path: ref.Path, Reference: ref}).Error
			})
		if err != nil {
			return err
		}
	}
	if err := e.ConstrainCopiedObservations(ctx, db, location); err != nil {
		return err
	}
	if err := e.stageSelectionCandidates(ctx, db, location); err != nil {
		return err
	}
	if err := observation.Match(ctx, db, nil); err != nil {
		return err
	}
	if err := e.validateSelectionStage(ctx, db, location); err != nil {
		return err
	}
	_, err = e.lib.PublishSelectedObservations(ctx, location.ID, location.Revision, func(ctx context.Context, yield func(*library.OnlinePosition) error) error {
		return eachSelectionObservation(ctx, db, func(item *observation.Item) error { return yield(item.Position(location.ID)) })
	})
	if err != nil {
		return err
	}

	// Freeze newly allocated identities while the Location gate still protects each published path.
	return eachSelectionObservation(ctx, db, func(item *observation.Item) error {
		original, err := e.lib.GetFileLocationAtPath(ctx, location.ID, item.Path)
		if err != nil {
			return err
		}
		if original == nil || (item.FileID != 0 && item.FileID != original.FileID) {
			return library.ErrOnlineConflict
		}
		return db.WithContext(ctx).Model(item).Update("file_id", original.FileID).Error
	})
}

func (e *Executor) stageSelectedFile(ctx context.Context, db *gorm.DB, location *library.Location, ref *entity.LocationEntryRef) error {
	_, name, info, err := e.ResolveLocationEntry(ctx, ref)
	if err != nil {
		return err
	}
	keys, err := ObserveTracking(location, name, info)
	if err != nil {
		return err
	}
	item := &observation.Item{Path: ref.Path, Reference: ref, Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano(), Evidence: observation.FromKeys(keys)}
	item.CopyResult, err = e.lib.CopyAdmission(ctx, location.ID, location.BindingToken, ref.Facts.Identity)
	if err != nil {
		return err
	}
	cache, valid, _ := acp.ReadCachedSignature(name)
	if valid && cache.Size == item.Size && cache.MtimeNS == item.MtimeNS {
		item.Hash = append([]byte(nil), cache.SHA256[:]...)
		item.Signature, err = library.NewFileSignature(item.Hash, item.Size)
		if err != nil {
			return err
		}
	}
	old, err := e.lib.GetFileLocationAtPath(ctx, location.ID, ref.Path)
	if err != nil {
		return err
	}
	if old != nil {
		p := item.Position(location.ID)
		preserveObservedSignature(p, old)
		item.Signature = p.Signature
		unchanged, err := e.lib.MatchesObservation(ctx, location, p)
		if err != nil {
			return err
		}
		if unchanged {
			item.Hash, item.Signature = old.Hash, old.Signature
		}
	}
	return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(item).Error
}

func eachSelectionObservation(ctx context.Context, db *gorm.DB, yield func(*observation.Item) error) error {
	after := ""
	for {
		var rows []*observation.Item
		if err := db.WithContext(ctx).Where("path > ?", after).Order("path").Limit(observation.BatchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err := yield(row); err != nil {
				return err
			}
		}
		after = rows[len(rows)-1].Path
	}
}

func (e *Executor) stageSelectionCandidates(ctx context.Context, db *gorm.DB, location *library.Location) error {
	// Lookup pages contain only evidence relevant to selected files; no global filesystem traversal.
	afterPath := ""
	for {
		var rows []*observation.Item
		if err := db.WithContext(ctx).Where("path > ?", afterPath).Order("path").Limit(admissionPageSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		var positions []*library.OnlinePosition
		var signatures [][]byte
		for _, row := range rows {
			positions = append(positions, row.Position(location.ID))
			if len(row.Signature) > 0 {
				signatures = append(signatures, row.Signature)
			}
			old, err := e.lib.GetFileLocationAtPath(ctx, location.ID, row.Path)
			if err != nil {
				return err
			}
			if old != nil {
				if err := e.stageSelectionCandidate(ctx, db, location, old.FileID, nil); err != nil {
					return err
				}
			}
		}
		for _, kind := range []library.TrackingKind{"signature", library.TrackingNative, library.TrackingUUID} {
			var afterID int64
			for {
				var keys []*library.FileTrackingKey
				if kind == "signature" {
					candidates, err := e.lib.AdmissionSignatureCandidates(ctx, location, signatures, afterID, admissionPageSize)
					if err != nil {
						return err
					}
					for _, candidate := range candidates {
						keys = append(keys, &library.FileTrackingKey{FileID: candidate.FileID})
					}
				} else {
					var err error
					keys, err = e.lib.AdmissionCandidates(ctx, location, kind, positions, afterID, admissionPageSize)
					if err != nil {
						return err
					}
				}
				if len(keys) == 0 {
					break
				}
				for _, key := range keys {
					if err := e.stageSelectionCandidate(ctx, db, location, key.FileID, key); err != nil {
						return err
					}
				}
				afterID = keys[len(keys)-1].FileID
			}
		}
		afterPath = rows[len(rows)-1].Path
	}
}

func (e *Executor) stageSelectionCandidate(ctx context.Context, db *gorm.DB, location *library.Location, fileID int64, key *library.FileTrackingKey) error {
	old, err := e.lib.GetFileLocation(ctx, fileID)
	if err != nil {
		return err
	}
	original := &observation.Original{FileID: fileID}
	if old != nil {
		if old.LocationID != location.ID {
			return nil
		}
		var observed int64
		if err := db.WithContext(ctx).Model(&observation.Item{}).Where("path = ?", old.Path).Count(&observed).Error; err != nil {
			return err
		}
		if observed == 0 && !e.absentAdmissionPath(location, old) {
			return nil
		}
		original.Path = old.Path
		if old.CurrentBinding(location) {
			original.Signature, original.Hash, original.Size = old.Signature, old.Hash, old.Size
		}
	}
	if err := db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(original).Error; err != nil {
		return err
	}
	if key == nil {
		return nil
	}
	var values map[string]any
	switch key.Kind {
	case library.TrackingNative:
		values = map[string]any{"native_scope": key.Scope, "native_key": key.KeyValue, "birth_ns": key.Details.BirthNS, "generation": key.Details.Generation}
	case library.TrackingUUID:
		values = map[string]any{"uuid_scope": key.Scope, "uuid": key.KeyValue}
	default:
		return nil
	}
	return db.WithContext(ctx).Model(original).Updates(values).Error
}

func (e *Executor) validateSelectionStage(ctx context.Context, db *gorm.DB, location *library.Location) error {
	if err := eachSelectionObservation(ctx, db, func(item *observation.Item) error {
		if _, _, _, err := e.ResolveLocationEntry(ctx, item.Reference); err != nil {
			return err
		}
		if item.FileID == 0 {
			return nil
		}
		old, err := e.lib.GetFileLocation(ctx, item.FileID)
		if err != nil {
			return err
		}
		if old != nil && old.Path != item.Path && !e.absentAdmissionPath(location, old) {
			return library.ErrOnlineConflict
		}
		return nil
	}); err != nil {
		return err
	}
	var after *string
	for {
		var rows []*selectionDirectory
		query := db.WithContext(ctx).Order("path").Limit(observation.BatchSize)
		if after != nil {
			query = query.Where("path > ?", *after)
		}
		if err := query.Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if _, _, _, err := e.ResolveLocationEntry(ctx, row.Reference); err != nil {
				return err
			}
		}
		after = &rows[len(rows)-1].Path
	}
}

func (e *Executor) yieldStagedSelections(ctx context.Context, db *gorm.DB, locationID int64, yield func(*library.File, string) error) error {
	return eachSelectionObservation(ctx, db, func(item *observation.Item) error {
		// Content preparation may follow this File's relocation, never a new File occupying its old path.
		if err := e.validateSelectedOriginal(ctx, locationID, item); err != nil {
			return err
		}
		if err := e.yieldSelectedFile(ctx, item.FileID, yield); err != nil {
			return err
		}
		return e.validateSelectedOriginal(ctx, locationID, item)
	})
}

func (e *Executor) validateSelectedOriginal(ctx context.Context, locationID int64, item *observation.Item) error {
	// A configuration change invalidates the original selection even if a later admission reused its File ID.
	if item.FileID <= 0 || item.Reference == nil || item.Reference.LocationId != locationID {
		return fmt.Errorf("selected observation has no frozen File identity")
	}
	source, err := e.lib.GetOnlineSource(ctx, locationID)
	if err != nil {
		return err
	}
	if source.Binding != entity.OnlineBinding_CONFIRMED || source.BindingToken != item.Reference.BindingToken {
		return library.ErrOnlineConflict
	}

	// Safe relocation retains the frozen object/content facts at the File's current confirmed original.
	original, err := e.lib.GetFileLocation(ctx, item.FileID)
	if err != nil {
		return err
	}
	if original == nil {
		return library.ErrOnlineConflict
	}
	_, _, _, err = e.ResolveLocationEntry(ctx, &entity.LocationEntryRef{LocationId: original.LocationID, Path: original.Path,
		BindingToken: original.ObservedBindingToken, Facts: item.Reference.Facts})
	return err
}
