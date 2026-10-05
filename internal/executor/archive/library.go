package archive

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
)

func (a *jobArchiveRunner) applyLibrarySpec(ctx context.Context, spec *entity.ArchiveJobSpec) error {
	// Preserve selected content identity across import while expanding the logical tree.
	if err := a.resetManifest(ctx); err != nil {
		return err
	}

	// Persist selected File facts and logical targets in bounded Job batches.
	batch := make([]*Item, 0, batchSize)
	var total, files int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}

		// Count only accepted unique content inputs; duplicate selection roots never inflate capacity.
		if err := a.db.WithContext(ctx).Create(&batch).Error; err != nil {
			return err
		}
		for _, item := range batch {
			files++
			total += item.Size
		}

		// Index every selected source Location for catalog Job searches.
		locations := make(map[int64]bool)
		var properties []executor.JobProperty
		for _, item := range batch {
			id := item.Data.Expected.GetOriginalLocationId()
			if id == 0 || locations[id] {
				continue
			}
			locations[id] = true
			properties = append(properties, executor.JobProperty{Key: executor.JobPropertyLocation, Value: id})
		}
		if err := a.exe.AddJobProperties(ctx, a.job.ID, properties...); err != nil {
			return err
		}
		batch = batch[:0]
		a.getProgress().SetGlobalTotal(total, files)
		return nil
	}
	// Capture current originals in bounded groups instead of repeating Catalog reads per File.
	var ids []int64
	targets := make(map[int64]string)
	capture := func() error {
		// The existing manifest owns cross-batch deduplication before live capture work.
		selected, err := a.uncapturedSelectionIDs(ctx, ids, targets)
		if err != nil {
			return err
		}
		if err := a.exe.CaptureOriginals(ctx, selected, func(id int64, filename string, expected *entity.ExpectedFile) error {
			batch = append(batch, &Item{Status: entity.CopyStatus_COPY_STATUS_PENDING, Size: expected.SizeBytes, TargetPath: targets[id], Data: &entity.ArchiveManifestFile{SourcePath: filename, Expected: expected, LibrarySelected: true}})
			return nil
		}); err != nil {
			return err
		}
		ids, targets = ids[:0], make(map[int64]string)
		return flush()
	}
	if err := a.exe.WalkLiveSelections(ctx, a.db, spec.Selections, func(file *library.File, target string) error {
		ids, targets[file.ID] = append(ids, file.ID), target
		if len(ids) == batchSize {
			return capture()
		}
		return nil
	}); err != nil {
		return err
	}
	if err := capture(); err != nil {
		return err
	}
	if files == 0 {
		return fmt.Errorf("Archive selection contains no regular files")
	}
	return nil
}

// uncapturedSelectionIDs preserves first eligible target ownership without retaining the manifest in memory.
func (a *jobArchiveRunner) uncapturedSelectionIDs(ctx context.Context, ids []int64, targets map[int64]string) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target)
	}
	var prior []*Item
	if err := a.db.WithContext(ctx).Select("target_path", "data").Where("target_path IN ?", paths).Find(&prior).Error; err != nil {
		return nil, err
	}
	seen := make(map[string]int64, len(prior)+len(ids))
	for _, item := range prior {
		seen[item.TargetPath] = item.Data.GetExpected().GetFileId()
	}

	// A target with another File is still a conflict; repeated identical selections do no I/O.
	selected := make([]int64, 0, len(ids))
	for _, id := range ids {
		target := targets[id]
		if previous, exists := seen[target]; exists {
			if previous != id {
				return nil, fmt.Errorf("Archive selection changed at logical target %q", target)
			}
			continue
		}
		seen[target] = id
		selected = append(selected, id)
	}
	return selected, nil
}
