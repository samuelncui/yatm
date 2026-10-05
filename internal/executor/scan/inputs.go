package scan

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *runner) prepareInputs(ctx context.Context, config *Config, target *entity.ReadMediaTarget) error {
	// Frozen Preview selections are read again but never admitted or expanded from mutable Library state.
	if config.IndexedInput {
		r.locations = []int64{0}
		if config.Spec.SignaturePolicy == entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ {
			if err := r.db.WithContext(ctx).Model(&Entry{}).Where("published = ?", false).Update("needs_hash", true).Error; err != nil {
				return err
			}
		}

		// Retain parent-owned Location identities in the companion's resource history.
		return r.eachEntry(ctx, &Scope{}, func(row *Entry) error {
			id := row.Expected.GetOriginalLocationId()
			if id <= 0 {
				return nil
			}
			return r.exe.AddJobProperties(ctx, r.job.ID, executor.JobProperty{Key: executor.JobPropertyLocation, Value: id})
		})
	}
	r.scopes, r.locations, r.sources = nil, nil, make(map[int64]*library.Location)
	if config.Spec.MediaId > 0 {
		r.locations = []int64{0}
		return r.freezeMedia(ctx, config)
	}
	if err := r.db.WithContext(ctx).Where("1 = 1").Delete(&Entry{}).Error; err != nil {
		return err
	}
	// Collapse overlapping Location roots before traversal; Library roots retain their own scope.
	var logical []*entity.FileSelection
	physical := make(map[int64][]string)
	for _, selection := range config.Spec.Selections {
		if local := selection.GetLocation(); local != nil {
			physical[local.LocationId] = append(physical[local.LocationId], local.Path)
			continue
		}
		logical = append(logical, selection)
	}
	locations := make([]int64, 0, len(physical))
	for id := range physical {
		locations = append(locations, id)
	}
	sort.Slice(locations, func(i, j int) bool { return locations[i] < locations[j] })
	for _, id := range locations {
		for _, scope := range analysisScopes(physical[id]) {
			scope.LocationID = id
			r.scopes = append(r.scopes, scope)
		}
		local, err := r.openLocation(ctx, &Scope{LocationID: id})
		if err != nil {
			return err
		}
		if err := local.enumerate(ctx, config.Spec.SignaturePolicy); err != nil {
			return err
		}
	}

	// Expand each logical selection once, writing its observations into the same bounded manifest.
	if err := r.prepareLogicalInputs(ctx, logical, config.Spec.SignaturePolicy); err != nil {
		return err
	}

	// Complete every selected range, including empty ranges, before content or Library publication.
	for _, id := range r.locations {
		local := &locationStage{runner: r, source: r.sources[id]}
		if err := local.reconcile(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) prepareLogicalInputs(ctx context.Context, selections []*entity.FileSelection, policy entity.ScanSignaturePolicy) error {
	return r.exe.Lib().WalkOriginalSelectionBatches(ctx, selections, func(files []*library.File, _ []string) error {
		// Validate every selected original, even when a physical root already observed its path.
		ids := make([]int64, 0, len(files))
		for _, file := range files {
			ids = append(ids, file.ID)
		}
		facts, err := r.exe.Lib().ReadFileFacts(ctx, ids, false, false)
		if err != nil {
			return err
		}
		batches := make(map[int64][]*Entry)
		originals := make(map[int64]map[string]*library.FileLocation)
		var locations []int64
		for _, file := range files {
			original := facts[file.ID].Original
			if original == nil {
				return fmt.Errorf("File %d has no original to scan", file.ID)
			}
			local, err := r.openLocation(ctx, &Scope{LocationID: original.LocationID})
			if err != nil {
				return err
			}
			if local.explicitScope(original.Path) != nil {
				continue
			}
			if originals[original.LocationID] == nil {
				originals[original.LocationID] = make(map[string]*library.FileLocation)
				locations = append(locations, original.LocationID)
			}
			originals[original.LocationID][original.Path] = original
			if err := walkSelection(ctx, r.exe, local.source, original.Path, func(name string, info os.FileInfo) error {
				row, err := local.observation(name, original.Path, info, policy)
				if err != nil {
					return err
				}
				batches[original.LocationID] = append(batches[original.LocationID], row)
				if len(batches[original.LocationID]) == batchSize {
					if err := local.saveObservations(ctx, batches[original.LocationID], originals[original.LocationID], policy); err != nil {
						return err
					}
					batches[original.LocationID] = batches[original.LocationID][:0]
				}
				return nil
			}); err != nil {
				return err
			}
		}

		// Reuse the original facts just read; physical and logical inputs share enrichment and writes.
		for _, id := range locations {
			local := &locationStage{runner: r, source: r.sources[id]}
			if err := local.saveObservations(ctx, batches[id], originals[id], policy); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *runner) resolveIndexedSource(ctx context.Context, expected *entity.ExpectedFile) (string, error) {
	// Preserve frozen and actually used resource history when a File is relocated before it is read.
	filename, locationID, err := r.exe.ResolveOriginal(ctx, expected)
	if err != nil {
		return "", err
	}
	if err := r.exe.AddJobProperties(ctx, r.job.ID, executor.JobProperty{Key: executor.JobPropertyLocation, Value: locationID}); err != nil {
		return "", err
	}
	return filename, nil
}

func (r *runner) freezeMedia(ctx context.Context, config *Config) error {
	// Freeze the expected catalog before physical observation, including explicit Tape selection.
	if _, err := r.validateMedia(ctx, config); err != nil {
		return err
	}
	if config.BaselineFrozen && config.Spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES {
		return nil
	}
	if err := r.db.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Entry{}).Error; err != nil {
		return err
	}
	if err := r.exe.Lib().SnapshotMediaPositions(ctx, config.Spec.MediaId, func(p *library.Position) error {
		change := entity.ScanChange_SCAN_CHANGE_REMOVED
		if config.Spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES {
			change = entity.ScanChange_SCAN_CHANGE_UNCHANGED
		}
		entry := &Entry{Path: p.Path, Change: change,
			Size: p.Size, Mode: p.Mode, MtimeNS: p.MtimeNS, PositionID: p.ID,
			Expected: &entity.ExpectedFile{Signature: p.Signature, Sha256: p.Hash, SizeBytes: p.Size, Mode: p.Mode, MtimeNs: p.MtimeNS},
			Storage:  &entity.StoragePosition{Order: p.StorageOrder, Metadata: p.StorageMetadata}, StorageOrder: append([]byte{}, p.StorageOrder...)}
		if config.Spec.ResultPolicy == entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES {
			// Result facts remain the frozen expectation; reads populate only the separate actual fields.
			entry.SHA256, entry.Signature = p.Hash, p.Signature
		}
		if err := r.db.WithContext(ctx).Create(entry).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	config.BaselineFrozen = true
	return r.db.WithContext(ctx).Model(config).Update("baseline_frozen", true).Error
}

// IndexedSource supplies an immutable content selection owned by a parent workflow.
type IndexedSource func(context.Context, func(string, *entity.ExpectedFile) error) error

func CreateIndexed(ctx context.Context, exe *executor.Executor, priority int64, spec *entity.ScanJobSpec, source IndexedSource) (*executor.Job, error) {
	// Companion scans share the runner and manifest, without rescanning or admitting their parent's inputs.
	if spec == nil || source == nil {
		return nil, fmt.Errorf("indexed Scan input is missing")
	}
	if !previewEnabled(spec.PreviewPolicy) {
		return nil, fmt.Errorf("indexed Scan requires Preview generation")
	}
	if _, ok := entity.PreviewPolicy_name[int32(spec.PreviewPolicy)]; !ok {
		return nil, fmt.Errorf("invalid Scan Preview policy")
	}
	if exe.Previews() == nil {
		return nil, fmt.Errorf("Preview module is not configured")
	}
	if err := exe.Previews().CheckGeneration(ctx); err != nil {
		return nil, err
	}
	spec = proto.Clone(spec).(*entity.ScanJobSpec)
	spec.ResultPolicy = entity.ScanResultPolicy_SCAN_RESULT_POLICY_REPORT_ONLY
	previewSettings, err := freezePreviewSettings(ctx, exe)
	if err != nil {
		return nil, err
	}
	return exe.CreateJob(ctx, entity.JobKind_JOB_KIND_SCAN, priority, func(db *gorm.DB) error {
		if err := prepareSchema(db); err != nil {
			return err
		}
		if err := db.Create(&Config{ID: 1, Spec: spec, IndexedInput: true, PreviewJobSettings: previewSettings}).Error; err != nil {
			return err
		}
		return source(ctx, func(filename string, expected *entity.ExpectedFile) error {
			if expected == nil || len(expected.Sha256) != 32 || expected.SizeBytes < 0 {
				return fmt.Errorf("indexed Scan expected content is incomplete")
			}
			if !exe.Previews().Supports(filename, previewSettings) {
				return nil
			}
			expected = proto.Clone(expected).(*entity.ExpectedFile)
			return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&Entry{Path: filename, SourcePath: filename,
				Change: entity.ScanChange_SCAN_CHANGE_UNCHANGED, Size: expected.SizeBytes, Mode: expected.Mode, MtimeNS: expected.MtimeNs,
				SHA256: expected.Sha256, Signature: expected.Signature, Expected: expected, NeedsHash: spec.SignaturePolicy != entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_KNOWN_ONLY}).Error
		})
	})
}

type locationStage struct {
	*runner
	source *library.Location
}

func (r *runner) openLocation(ctx context.Context, scope *Scope) (*locationStage, error) {
	// All selected paths share one admitted Location and its recorded attempt.
	if source := r.sources[scope.LocationID]; source != nil {
		return &locationStage{runner: r, source: source}, nil
	}
	source, err := r.exe.Lib().RecordLocationAttempt(ctx, scope.LocationID, r.job.ID)
	if err != nil {
		return nil, err
	}
	if err := r.exe.AddJobProperties(ctx, r.job.ID, executor.JobProperty{Key: executor.JobPropertyLocation, Value: source.ID}); err != nil {
		return nil, err
	}
	if _, err := r.exe.CheckLocation(source); err != nil {
		return nil, err
	}
	r.sources[source.ID] = source
	r.locations = append(r.locations, source.ID)
	return &locationStage{runner: r, source: source}, nil
}
