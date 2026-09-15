package archive

import (
	"context"
	"fmt"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"google.golang.org/protobuf/proto"
)

func (a *jobArchiveRunner) applyLibrarySpec(ctx context.Context, spec *entity.ArchiveJobSpec) error {
	// Preserve selected content identity across import while expanding the logical tree.
	if len(spec.Sources) != 0 {
		return fmt.Errorf("Archive Sources and Library File IDs are mutually exclusive")
	}
	release, err := a.exe.Lib().UseOnlineRead()
	if err != nil {
		return err
	}
	defer release()
	if err := a.resetManifest(ctx); err != nil {
		return err
	}

	// Persist complete frozen integrity facts and logical targets in bounded Job batches.
	batch := make([]*Item, 0, batchSize)
	var total, files int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}

		// Repeated physical/logical roots deduplicate through the durable target index, with frozen identity guards.
		paths := make([]string, 0, len(batch))
		for _, item := range batch {
			paths = append(paths, item.TargetPath)
		}
		var prior []*Item
		if err := a.db.WithContext(ctx).Where("target_path IN ?", paths).Find(&prior).Error; err != nil {
			return err
		}
		seen := make(map[string]*entity.ExpectedFile, len(prior)+len(batch))
		for _, item := range prior {
			seen[item.TargetPath] = item.Data.Expected
		}
		accepted := batch[:0]
		for _, item := range batch {
			if expected, exists := seen[item.TargetPath]; exists {
				if !proto.Equal(expected, item.Data.Expected) {
					return fmt.Errorf("Archive selection changed at logical target %q", item.TargetPath)
				}
				continue
			}
			seen[item.TargetPath] = item.Data.Expected
			accepted = append(accepted, item)
		}
		batch = accepted
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
		locations := make(map[int64]bool)
		var resources []executor.JobResource
		for _, item := range batch {
			id := item.Data.Expected.GetOriginalLocationId()
			if id == 0 || locations[id] {
				continue
			}
			locations[id] = true
			resources = append(resources, executor.JobResource{Kind: executor.JobResourceLocation, Role: executor.JobResourceSource, ResourceID: id})
		}
		if err := a.exe.AddJobResources(ctx, a.job.ID, resources...); err != nil {
			return err
		}
		batch = batch[:0]
		a.getProgress().SetGlobalTotal(total, files)
		return nil
	}
	selections := spec.Selections
	for _, id := range spec.FileIds {
		selections = append(selections, &entity.FileSelection{Target: &entity.FileSelection_Library{Library: &entity.LibrarySelection{FileId: id}}, Scope: entity.FileScope_FILE_SCOPE_ALL})
	}
	if err := a.exe.WalkLiveSelections(ctx, a.db, selections, func(file *library.File, target string) error {
		// Choose a verified current original, without Restore or implicit omissions.
		filename, expected, err := a.exe.CaptureOriginal(ctx, file.ID)
		if err != nil {
			return err
		}
		batch = append(batch, &Item{Status: entity.CopyStatus_PENDING, Size: expected.Size, TargetPath: target, Data: &entity.ArchiveManifestFile{SourcePath: filename, Expected: expected, LibrarySelected: true}})
		if len(batch) == batchSize {
			return flush()
		}
		return nil
	}); err != nil {
		return err
	}
	if err := flush(); err != nil {
		return err
	}
	if files == 0 {
		return fmt.Errorf("Archive selection contains no regular files")
	}
	return nil
}
