package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor/observation"
	"github.com/samuelncui/yatm/internal/library"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// selectionCandidate keeps only relevant IDs in the request-owned preparation database.
type selectionCandidate struct {
	FileID int64 `gorm:"primaryKey;autoIncrement:false"`
}

func (selectionCandidate) TableName() string { return "candidate_ids" }

func (e *Executor) stageLiveSelections(ctx context.Context, db *gorm.DB, locationID int64, roots []*entity.LocationSelection) error {
	// Preparing a Job manifest holds the shared read admission, not the Location itself.
	location, err := e.lib.GetLocation(ctx, locationID)
	if err != nil {
		return err
	}
	if _, err := e.CheckLocation(location); err != nil {
		return err
	}
	// These request-owned rows retain observations and relevant IDs, never a copy of Library facts.
	if err := db.WithContext(ctx).AutoMigrate(&selectionCandidate{}); err != nil {
		return err
	}
	for _, model := range []any{&observation.Item{}, &selectionCandidate{}} {
		if err := db.WithContext(ctx).Where("1 = 1").Delete(model).Error; err != nil {
			return err
		}
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
		_, info, err := e.checkPreparedLocationPath(location, location.RootPath, root.Path)
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
				item, err := selectedFileObservation(location, ref, info)
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
	if err := e.stageSelectionCandidateIDs(ctx, db, location); err != nil {
		return err
	}
	candidates := func(ctx context.Context, after int64, limit int) ([]*observation.Original, error) {
		var ids []int64
		if err := db.WithContext(ctx).Model(&selectionCandidate{}).Where("file_id > ?", after).
			Order("file_id").Limit(limit).Pluck("file_id", &ids).Error; err != nil {
			return nil, err
		}
		rows, err := e.lib.MatchingCandidates(ctx, ids)
		if err != nil {
			return nil, err
		}
		paths := make([]string, 0, len(rows))
		for _, row := range rows {
			if row.Original != nil {
				paths = append(paths, row.Original.Path)
			}
		}
		var observedPaths []string
		if len(paths) > 0 {
			if err := db.WithContext(ctx).Model(&observation.Item{}).Where("path IN ? AND directory = ?", paths, false).Pluck("path", &observedPaths).Error; err != nil {
				return nil, err
			}
		}
		observed := make(map[string]bool, len(observedPaths))
		for _, name := range observedPaths {
			observed[name] = true
		}
		values := make([]*observation.Original, 0, len(rows))
		for _, row := range rows {
			value := &observation.Original{FileID: row.FileID}
			values = append(values, value)
			if old := row.Original; old != nil {
				if !observed[old.Path] && !e.absentAdmissionPath(location, old) {
					continue
				}
				value.Path = old.Path
				if old.LocationID == location.ID {
					value.Signature, value.Hash, value.Size = old.Signature, old.Hash, old.Size
				}
			}
			value.Evidence = observation.FromKeys(row.Tracking)
		}
		return values, nil
	}
	store := observation.NewMatchStore(db.Model(&observation.Item{}).Where("directory = ?", false))
	if err := observation.Match(ctx, store, candidates, nil); err != nil {
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

func (e *Executor) stageSelectionCandidateIDs(ctx context.Context, db *gorm.DB, location *library.Location) error {
	// Keep matching work proportional to selected evidence, while ID order preserves global rule precedence.
	store := func(ids []int64) error {
		if len(ids) == 0 {
			return nil
		}
		rows := make([]selectionCandidate, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, selectionCandidate{FileID: id})
		}
		return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
	}
	var after string
	for {
		var rows []*observation.Item
		if err := db.WithContext(ctx).Where("directory = ? AND path > ?", false, after).
			Order("path").Limit(observation.BatchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		var ids []int64
		paths := make([]string, 0, len(rows))
		positions := make([]*library.ObservedEntry, 0, len(rows))
		var signatures [][]byte
		for _, row := range rows {
			positions = append(positions, row.Position())
			if len(row.Signature) > 0 {
				signatures = append(signatures, row.Signature)
			}
			paths = append(paths, row.Path)
		}
		originals, err := e.lib.ReadFileOriginalsAt(ctx, location.ID, paths)
		if err != nil {
			return err
		}
		for _, old := range originals {
			ids = append(ids, old.FileID)
		}
		if err := store(ids); err != nil {
			return err
		}

		// Only equal selected signatures and tracking evidence admit relocation candidates.
		var cursor int64
		for {
			matches, err := e.lib.AdmissionSignatureCandidates(ctx, location, signatures, cursor, observation.BatchSize)
			if err != nil {
				return err
			}
			if len(matches) == 0 {
				break
			}
			ids = ids[:0]
			for _, match := range matches {
				ids = append(ids, match.FileID)
			}
			if err := store(ids); err != nil {
				return err
			}
			cursor = matches[len(matches)-1].FileID
		}
		cursor = 0
		for {
			keys, err := e.lib.AdmissionCandidates(ctx, location, library.TrackingNative, positions, cursor, observation.BatchSize)
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				break
			}
			ids = ids[:0]
			for _, key := range keys {
				ids = append(ids, key.FileID)
			}
			if err := store(ids); err != nil {
				return err
			}
			cursor = keys[len(keys)-1].FileID
		}
		after = rows[len(rows)-1].Path
	}
}

func selectedFileObservation(location *library.Location, ref *entity.LocationEntryRef, info os.FileInfo) (*observation.Item, error) {
	// Evidence consumes the metadata already observed during traversal.
	facts, err := InspectLocationFacts(info)
	if err != nil {
		return nil, err
	}
	name := filepath.Join(location.RootPath, filepath.FromSlash(ref.Path))
	keys := ObserveTracking(location, info)
	item := &observation.Item{Path: ref.Path, Reference: ref, Size: facts.SizeBytes, Mode: facts.Mode, MtimeNS: facts.MtimeNs, Evidence: observation.FromKeys(keys)}
	cache, valid, _ := acp.ReadCachedSignature(name)
	if valid && cache.Size == item.Size && cache.MtimeNS == item.MtimeNS {
		item.Hash = append([]byte(nil), cache.SHA256[:]...)
		signature, err := library.NewFileSignature(item.Hash, item.Size)
		if err != nil {
			return nil, err
		}
		item.Signature = signature
	}
	return item, nil
}

func (e *Executor) stageSelectedFiles(ctx context.Context, db *gorm.DB, location *library.Location, rows []*observation.Item) error {
	if len(rows) == 0 {
		return nil
	}
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
	return db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
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
