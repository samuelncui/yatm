package restore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"path"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// applySpec freezes explicit archived versions; current originals never choose restore content.
func (a *jobRestoreRunner) applySpec(ctx context.Context, spec *entity.RestoreJobSpec) (returnErr error) {
	// Exclude Library replacement for the complete manifest-building attempt.
	if spec == nil || (len(spec.FileVersionIds) == 0 && len(spec.Selections) == 0) {
		return fmt.Errorf("Restore requires FileVersions or file selections")
	}
	release, err := a.destinationAdmission()
	if err != nil {
		return err
	}
	defer release()
	if _, err := a.exe.RestoreOutputPath(ctx, spec.Destination, ""); err != nil {
		return err
	}

	// Select all versions before deciding which one may reconnect the source File.
	names, err := newOutputNames(ctx, a)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, names.close()) }()
	if err := names.rebuild(ctx); err != nil {
		return err
	}
	var config Config
	if err := a.db.WithContext(ctx).First(&config, 1).Error; err != nil {
		return err
	}
	if !config.ManifestFrozen {
		a.dropProgress()
		progress := a.getProgress()
		if err := a.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error { return a.buildManifest(ctx, spec, tx, progress) }); err != nil {
			return err
		}
	}
	if err := a.prepareOutputs(ctx, names); err != nil {
		return err
	}
	if err := a.publishReadyOutputs(ctx, true); err != nil {
		return err
	}
	a.dropProgress()
	return nil
}

func (a *jobRestoreRunner) buildManifest(ctx context.Context, spec *entity.RestoreJobSpec, db *gorm.DB, progress *executor.Progress) error {
	// Rebuild only incomplete manifests; policy mismatches never freeze a partial executable selection.
	if err := library.ValidateRestoreVersionSelection(spec.VersionPolicy, spec.SkipUnmatchedVersions); err != nil {
		return err
	}
	if err := a.resetManifest(ctx, db); err != nil {
		return err
	}
	var totalFiles, totalBytes int64
	resources := make([]executor.JobResource, 0, batchSize)
	seenMedia := make(map[int64]bool, batchSize)
	flushResources := func() error {
		if err := a.exe.AddJobResources(ctx, a.job.ID, resources...); err != nil {
			return err
		}
		resources = resources[:0]
		seenMedia = make(map[int64]bool, batchSize)
		return nil
	}
	appendVersion := func(item *library.RestoreSelectionItem) error {
		// Missing policy matches require explicit omission; unavailable physical copies never do.
		version := item.Version
		if version == nil {
			if spec.SkipUnmatchedVersions {
				a.logger.Infof("Skipping File %d: no version matches the restore cutoff (%s)", item.File.ID, item.Resolution.Match)
				return nil
			}
			return fmt.Errorf("File %d has no matching restore version (%s)", item.File.ID, item.Resolution.Match)
		}
		id := version.ID
		if !version.HasRestoreIntegrity() {
			return fmt.Errorf("FileVersion %d has no supported restore integrity facts", id)
		}

		// Freeze logical output names and reserve a collision-safe destination for this version.
		target := path.Clean(item.Path)
		if err := entity.ValidateRelativePath(target); err != nil {
			return err
		}
		file := item.File

		// Page independent physical candidates without changing version ownership.
		var found bool
		passes := 1
		if spec.AllowDamagedCopies {
			passes = 2
		}
		for pass := 0; pass < passes; pass++ {
			var after int64
			for {
				positions, more, err := a.exe.Lib().ListContentCopies(ctx, version.Signature, after, batchSize)
				if err != nil {
					return err
				}
				copies := make([]*Copy, 0, len(positions))
				for _, position := range positions {
					eligible := library.PositionRestoreEligible(position.Health)
					if (pass == 0) != eligible {
						continue
					}
					if !eligible && position.Health != entity.PositionHealth_DAMAGED && position.Health != entity.PositionHealth_UNREADABLE {
						continue
					}
					if position.Size != version.Size || !bytes.Equal(position.Hash, version.Hash) {
						return fmt.Errorf("Position %d disagrees with FileVersion %d integrity facts", position.ID, id)
					}
					if err := entity.ValidateRelativePath(position.Path); err != nil {
						return err
					}
					media, err := a.exe.Lib().GetMedia(ctx, position.MediaID)
					if err != nil {
						return err
					}
					token, err := library.PositionContentToken(position)
					if err != nil {
						return err
					}
					copies = append(copies, &Copy{
						ItemID: id, FileID: version.FileID, FileVersionID: id, Signature: version.Signature,
						Size: version.Size, Hash: version.Hash, Mode: version.Mode, MtimeNS: version.MtimeNS,
						Status: entity.CopyStatus_PENDING, TargetPath: target, MediaID: position.MediaID,
						MediaPath: position.Path, StorageOrder: append([]byte{}, position.StorageOrder...),
						MediaIdentity: media.Identity, MediaProfile: media.Profile,
						PositionID: position.ID, PositionContentToken: token,
						LibraryParentID: file.ParentID, LibraryName: file.Name, LastArchivedAt: version.LastArchivedAt})
					if !seenMedia[position.MediaID] {
						seenMedia[position.MediaID] = true
						resources = append(resources, executor.JobResource{
							Kind: executor.JobResourceMedia, Role: executor.JobResourceSource, ResourceID: position.MediaID})
						if len(resources) == batchSize {
							if err := flushResources(); err != nil {
								return err
							}
						}
					}
				}
				if len(copies) > 0 {
					found = true
					if err := db.Clauses(clause.OnConflict{
						Columns: []clause.Column{{Name: "item_id"}, {Name: "media_id"}}, DoNothing: true,
					}).Create(&copies).Error; err != nil {
						return err
					}
				}
				if !more {
					break
				}
				after = positions[len(positions)-1].ID
			}
		}

		// A version without any known copy cannot become an executable restore item.
		if !found {
			return fmt.Errorf("FileVersion %d has no archived copy", id)
		}
		if totalFiles == math.MaxInt64 || version.Size > math.MaxInt64-totalBytes {
			return fmt.Errorf("Restore totals exceed the supported range")
		}
		totalFiles++
		totalBytes += version.Size
		progress.SetGlobalTotal(totalBytes, totalFiles)
		return nil
	}

	// Resolve choices through the same paged policy walker used by the waitlist estimate.
	if err := a.exe.Lib().WalkRestoreSelections(ctx, spec.Selections, spec.FileVersionIds, spec.VersionPolicy, appendVersion); err != nil {
		return err
	}
	if totalFiles == 0 {
		return fmt.Errorf("Restore selection contains no matching saved versions")
	}
	if err := flushResources(); err != nil {
		return err
	}
	if err := freezeFileSelections(db); err != nil {
		return err
	}
	return db.Model(&Config{}).Where("id = ?", 1).Update("manifest_frozen", true).Error
}

func (a *jobRestoreRunner) resetManifest(ctx context.Context, db *gorm.DB) error {
	// An unfinished index attempt restarts from the complete saved specification.
	if err := db.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Copy{}).Error; err != nil {
		return err
	}
	return nil
}

func uniquePositiveIDs(ids []int64) []int64 {
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, found := seen[id]; found {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}
