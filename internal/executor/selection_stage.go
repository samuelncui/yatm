package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor/observation"
	"github.com/samuelncui/yatm/internal/library"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (e *Executor) stageLiveSelections(ctx context.Context, db *gorm.DB, locationID int64, roots []*entity.LocationSelection) error {
	// Preparing a Job manifest holds the shared read admission, not the Location itself.
	location, err := e.lib.GetLocation(ctx, locationID)
	if err != nil {
		return err
	}
	if _, err := e.CheckLocation(location); err != nil {
		return err
	}
	tracker := NewTracker()
	// These request-owned rows retain observations, never a copy of Library facts.
	if err := db.WithContext(ctx).Where("1 = 1").Delete(&observation.Item{}).Error; err != nil {
		return err
	}
	// Persist directory and file observations in bounded writes; catalog facts are joined per batch.
	batch := make([]*observation.Item, 0, observation.BatchSize)
	flush := func() error {
		if err := e.stageSelectedFiles(ctx, db, location, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	appendItem := func(item *observation.Item) error {
		batch = append(batch, item)
		if len(batch) == observation.BatchSize {
			return flush()
		}
		return nil
	}
	var covered []string
	for _, root := range roots {
		_, info, err := e.CheckPreparedLocationPath(location, location.RootPath, root.Path)
		if err != nil {
			return err
		}
		entry, err := locationEntry(location.ID, root.Path, info)
		if err != nil {
			return err
		}
		ref := entry.Reference
		err = e.walkLocationSelection(ctx, location, ref, info, false, 0, covered,
			func(ref *entity.LocationEntryRef, info os.FileInfo) error {
				item, err := selectedFileObservation(tracker, location, ref, info)
				if err != nil {
					return err
				}
				return appendItem(item)
			},
			func(ref *entity.LocationEntryRef) error {
				return appendItem(&observation.Item{Path: ref.Path, Reference: ref, Directory: true})
			})
		if err != nil {
			return err
		}
		covered = append(covered, root.Path)
	}
	if err := flush(); err != nil {
		return err
	}
	store := observation.NewMatchStore(db.Model(&observation.Item{}).Where("directory = ?", false))
	if err := e.MatchLocationObservations(ctx, location, store, nil); err != nil {
		return err
	}
	_, err = e.lib.PublishSelectedObservations(ctx, location.ID, func(ctx context.Context, yield func(*library.ObservedEntry) error) error {
		return eachSelectionObservation(ctx, db, func(item *observation.Item) error { return yield(item.Position()) })
	})
	if err != nil {
		return err
	}

	// Freeze allocated identities with one catalog lookup and staging write per batch.
	return eachSelectionObservationBatch(ctx, db, func(rows []*observation.Item) error {
		paths := make([]string, 0, len(rows))
		for _, item := range rows {
			paths = append(paths, item.Path)
		}
		originals, err := e.lib.ReadFileOriginalsAt(ctx, location.ID, paths)
		if err != nil {
			return err
		}
		for _, item := range rows {
			original := originals[item.Path]
			if original == nil || item.FileID != 0 && item.FileID != original.FileID {
				return library.ErrLocationConflict
			}
			item.FileID = original.FileID
		}
		return db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "path"}}, DoUpdates: clause.AssignmentColumns([]string{"file_id"})}).Create(&rows).Error
	})
}

func selectedFileObservation(tracker *Tracker, location *library.Location, ref *entity.LocationEntryRef, info os.FileInfo) (*observation.Item, error) {
	// Evidence consumes the metadata already observed during traversal.
	facts, err := InspectLocationFacts(info)
	if err != nil {
		return nil, err
	}
	name := filepath.Join(location.RootPath, filepath.FromSlash(ref.Path))
	keys := tracker.Observe(name, info)
	item := &observation.Item{Path: ref.Path, Reference: ref, Size: facts.SizeBytes, Mode: facts.Mode, MtimeNS: facts.MtimeNs, Evidence: observation.FromKeys(keys)}

	// Selection and direct admission share the same optional cache applicability rule.
	item.Hash, item.Signature, err = cachedObservedContent(name, item.Size, item.MtimeNS)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (e *Executor) stageSelectedFiles(ctx context.Context, db *gorm.DB, location *library.Location, rows []*observation.Item) error {
	if len(rows) == 0 {
		return nil
	}
	if err := e.enrichSelectedFiles(ctx, location, rows); err != nil {
		return err
	}
	return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// enrichSelectedFiles keeps the recorded content facts of an original observed unchanged at its path.
func (e *Executor) enrichSelectedFiles(ctx context.Context, location *library.Location, rows []*observation.Item) error {
	paths := make([]string, 0, len(rows))
	for _, item := range rows {
		if !item.Directory {
			paths = append(paths, item.Path)
		}
	}
	originals, err := e.lib.ReadFileOriginalsAt(ctx, location.ID, paths)
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(originals))
	for _, old := range originals {
		ids = append(ids, old.FileID)
	}
	keys, err := e.lib.ReadFileTrackingKeys(ctx, ids)
	if err != nil {
		return err
	}
	for _, item := range rows {
		old := originals[item.Path]
		if item.Directory || old == nil {
			continue
		}
		p := item.Position()
		preserveObservedSignature(p, old)
		item.Signature = p.Signature
		if library.MatchesOriginalObservation(old, p, keys[old.FileID]) {
			item.Hash, item.Signature = old.Hash, old.Signature
		}
	}
	return nil
}

func eachSelectionObservation(ctx context.Context, db *gorm.DB, yield func(*observation.Item) error) error {
	return eachSelectionObservationBatch(ctx, db, func(rows []*observation.Item) error {
		for _, row := range rows {
			if err := yield(row); err != nil {
				return err
			}
		}
		return nil
	})
}

func eachSelectionObservationBatch(ctx context.Context, db *gorm.DB, yield func([]*observation.Item) error) error {
	after := ""
	for {
		var rows []*observation.Item
		if err := db.WithContext(ctx).Where("directory = ? AND path > ?", false, after).Order("path").Limit(observation.BatchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		if err := yield(rows); err != nil {
			return err
		}
		after = rows[len(rows)-1].Path
	}
}

func (e *Executor) yieldStagedSelections(ctx context.Context, db *gorm.DB, yield func(*library.File, string) error) error {
	return eachSelectionObservationBatch(ctx, db, func(rows []*observation.Item) error {
		ids := make([]int64, 0, len(rows))
		for _, item := range rows {
			ids = append(ids, item.FileID)
		}
		byID, err := e.lib.ReadFileRows(ctx, ids)
		if err != nil {
			return err
		}
		files := make([]*library.File, 0, len(rows))
		for _, item := range rows {
			file := byID[item.FileID]
			if file == nil {
				return library.ErrFileNotFound
			}
			files = append(files, file)
		}
		paths, err := e.lib.ReadFilePaths(ctx, files)
		if err != nil {
			return err
		}
		for _, file := range files {
			if err := yield(file, strings.TrimPrefix(paths[file.ID], "/")); err != nil {
				return err
			}
		}
		return nil
	})
}
