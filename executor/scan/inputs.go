package scan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (r *runner) prepareInputs(ctx context.Context, config *Config, target *entity.ReadMediaTarget) error {
	// Frozen Preview selections are read again but never admitted or expanded from mutable Library state.
	if config.IndexedInput {
		return r.eachEntry(ctx, &Scope{}, func(row *Entry) error {
			id := row.Expected.GetOriginalLocationId()
			if id <= 0 {
				return nil
			}
			return r.exe.AddJobResources(ctx, r.job.ID, executor.JobResource{Kind: executor.JobResourceLocation, ResourceID: id, Role: executor.JobResourceSource})
		})
	}
	if config.Spec.MediaId > 0 {
		if config.MediaKind == entity.MediaKind_MEDIA_KIND_TAPE {
			return nil
		}
		return r.freezeMedia(ctx, config)
	}
	if err := r.db.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Scope{}).Error; err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Entry{}).Error; err != nil {
		return err
	}
	if config.Spec.LocationId > 0 {
		for _, scope := range analysisScopes(config.Spec.Paths) {
			if err := r.appendScope(ctx, config.Spec.LocationId, scope.Path); err != nil {
				return err
			}
		}
		return nil
	}

	// Expand Library directory selections with its bounded query contract, without admission or hashes.
	var logical []*entity.FileSelection
	for _, selection := range config.Spec.Selections {
		if local := selection.GetLocation(); local != nil {
			if local.Reference != nil {
				if _, _, _, err := r.exe.ResolveLocationEntry(ctx, local.Reference); err != nil {
					return err
				}
			}
			if err := r.appendScope(ctx, local.LocationId, local.Path); err != nil {
				return err
			}
			continue
		}
		logical = append(logical, selection)
	}
	return r.exe.Lib().WalkFileSelections(ctx, logical, func(file *library.File, _ string) error {
		original, err := r.exe.Lib().GetFileLocation(ctx, file.ID)
		if err != nil {
			return err
		}
		if original == nil {
			return fmt.Errorf("File %d has no original to scan", file.ID)
		}
		return r.appendScope(ctx, original.LocationID, original.Path)
	})
}

func (r *runner) resolveIndexedSource(ctx context.Context, expected *entity.ExpectedFile) (string, error) {
	// Preserve frozen and actually used resource history when the same File is relocated before retry.
	filename, locationID, err := r.exe.ResolveOnlineFile(ctx, expected)
	if err != nil {
		return "", err
	}
	if err := r.exe.AddJobResources(ctx, r.job.ID, executor.JobResource{Kind: executor.JobResourceLocation, ResourceID: locationID, Role: executor.JobResourceSource}); err != nil {
		return "", err
	}
	return filename, nil
}

func (r *runner) appendScope(ctx context.Context, locationID int64, path string) error {
	// Collapse expanded selections in the Job database, keeping memory bounded by the input page.
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&Scope{}).Where("location_id = ? AND (path = '' OR path = ? OR substr(?,1,length(path)+1) = path || '/')", locationID, path, path).Count(&count).Error; err != nil {
			return err
		}
		if count > 0 {
			return nil
		}
		if err := tx.Where("location_id = ? AND (? = '' OR substr(path,1,?) = ?)", locationID, path, len(path)+1, path+"/").Delete(&Scope{}).Error; err != nil {
			return err
		}
		return tx.Create(&Scope{LocationID: locationID, Path: path}).Error
	})
}

func (r *runner) freezeMedia(ctx context.Context, config *Config) error {
	// Freeze the expected catalog before any physical observation; retries of Tape reads retain this baseline.
	if _, err := r.validateMedia(ctx, config); err != nil {
		return err
	}
	if config.BaselineFrozen && config.Spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES {
		return nil
	}
	if err := r.db.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Entry{}).Error; err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Scope{}).Error; err != nil {
		return err
	}
	if err := r.db.WithContext(ctx).Create(&Scope{}).Error; err != nil {
		return err
	}
	if err := r.exe.Lib().SnapshotMediaPositions(ctx, config.Spec.MediaId, func(p *library.Position) error {
		token, err := library.PositionContentToken(p)
		if err != nil {
			return err
		}
		change := entity.ScanChange_SCAN_CHANGE_REMOVED
		if config.Spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES {
			change = entity.ScanChange_SCAN_CHANGE_UNCHANGED
		}
		entry := &Entry{Path: p.Path, Change: change,
			Size: p.Size, Mode: p.Mode, MtimeNs: p.ModTime.UnixNano(), PositionID: p.ID, ContentToken: token,
			Expected: &entity.ExpectedFile{Signature: p.Signature, Sha256: p.Hash, Size: p.Size, Mode: p.Mode, MtimeNs: p.ModTime.UnixNano()},
			Storage:  &entity.StoragePosition{Order: p.StorageOrder, Metadata: p.StorageMetadata}, StorageOrder: append([]byte{}, p.StorageOrder...)}
		if config.Spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES {
			// Result facts remain the frozen expectation; reads populate only the separate actual fields.
			entry.SHA256, entry.Signature = p.Hash, p.Signature
		}
		return r.db.WithContext(ctx).Create(entry).Error
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
	if spec.PreviewPolicy == entity.PreviewPolicy_PREVIEW_NONE {
		return nil, fmt.Errorf("indexed Scan requires Preview generation")
	}
	if _, ok := entity.PreviewPolicy_name[int32(spec.PreviewPolicy)]; !ok {
		return nil, fmt.Errorf("invalid Scan Preview policy")
	}
	if exe.Previews() == nil {
		return nil, fmt.Errorf("Preview module is not configured")
	}
	spec = proto.Clone(spec).(*entity.ScanJobSpec)
	spec.ResultPolicy = entity.ScanResultPolicy_REPORT_ONLY
	return exe.CreateJob(ctx, entity.JobKind_SCAN, priority, func(db *gorm.DB) error {
		if err := prepareSchema(db); err != nil {
			return err
		}
		if err := db.Create(&Config{ID: 1, Spec: spec, IndexedInput: true}).Error; err != nil {
			return err
		}
		if err := db.Create(&Scope{}).Error; err != nil {
			return err
		}
		return source(ctx, func(filename string, expected *entity.ExpectedFile) error {
			if expected == nil || len(expected.Sha256) != 32 || expected.Size < 0 {
				return fmt.Errorf("indexed Scan expected content is incomplete")
			}
			if !exe.Previews().Supports(filename) {
				return nil
			}
			info, err := os.Lstat(filename)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() != expected.Size || (expected.MtimeNs != 0 && expected.MtimeNs != info.ModTime().UnixNano()) {
				return fmt.Errorf("indexed Scan source facts changed: %q", filename)
			}
			expected = proto.Clone(expected).(*entity.ExpectedFile)
			expected.Mode, expected.MtimeNs = uint32(info.Mode()), info.ModTime().UnixNano()
			return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&Entry{Path: filename, SourcePath: filename,
				Change: entity.ScanChange_SCAN_CHANGE_UNCHANGED, Size: expected.Size, Mode: expected.Mode, MtimeNs: expected.MtimeNs,
				SHA256: expected.Sha256, Signature: expected.Signature, Expected: expected, NeedsHash: spec.SignaturePolicy != entity.ScanSignaturePolicy_KNOWN_ONLY}).Error
		})
	})
}

type locationStage struct {
	*runner
	source   *library.Location
	rootInfo os.FileInfo
	release  func()
}

func (r *runner) openLocation(ctx context.Context, scope *Scope) (*locationStage, error) {
	// Operation admission spans observation through metadata publication, not ordinary browsing.
	release := r.reservation
	r.reservation = nil
	if release == nil {
		var err error
		release, err = r.exe.Lib().UseOnlineSource(scope.LocationID)
		if err != nil {
			return nil, err
		}
	}
	source, err := r.exe.Lib().GetOnlineSource(ctx, scope.LocationID)
	if err != nil {
		release()
		return nil, err
	}
	source, err = r.exe.Lib().RecordOnlineAttempt(ctx, source.ID, r.job.ID)
	if err != nil {
		release()
		return nil, err
	}
	if err := r.exe.AddJobResources(ctx, r.job.ID, executor.JobResource{Kind: executor.JobResourceLocation, ResourceID: source.ID, Role: executor.JobResourceSource}); err != nil {
		release()
		return nil, err
	}
	root, err := r.exe.CheckOnlineSource(source)
	if err != nil {
		release()
		return nil, err
	}
	info, err := os.Stat(root)
	if err != nil {
		release()
		return nil, err
	}
	stage := &locationStage{runner: r, source: source, rootInfo: info, release: release}
	scope.Snapshot = source.CatalogEntity()
	scope.Error = ""
	if err := r.db.WithContext(ctx).Model(&Scope{}).Where("location_id = ?", source.ID).Updates(map[string]any{"snapshot": scope.Snapshot, "error": "", "published_at": 0}).Error; err != nil {
		release()
		return nil, err
	}
	return stage, nil
}

func (s *locationStage) enumerateInput(ctx context.Context, config *Config) error {
	// Temporary observations are the existing matcher projection, not another Job kind or result manifest.
	for _, model := range []any{&Item{}, &Original{}} {
		if err := s.db.WithContext(ctx).Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(model).Error; err != nil {
			return err
		}
	}
	if err := s.enumerate(ctx, s.source, config.Spec.SignaturePolicy); err != nil {
		return err
	}
	if err := s.reconcile(ctx, s.source, config.Spec.SignaturePolicy == entity.ScanSignaturePolicy_FORCE_READ); err != nil {
		return err
	}
	return s.copyObservations(ctx)
}

func (s *locationStage) copyObservations(ctx context.Context) error {
	// Project the selected source into the same entries consumed by every subsequent pipeline stage.
	var after int64
	for {
		var items []*Item
		if err := s.db.WithContext(ctx).Where("id > ?", after).Order("id").Limit(batchSize).Find(&items).Error; err != nil {
			return err
		}
		if len(items) == 0 {
			return nil
		}
		for _, item := range items {
			row := &Entry{LocationID: s.source.ID, Path: item.Path, ScopePath: item.ScopePath, Change: item.Change,
				Size: item.Size, Mode: item.Mode, MtimeNs: item.MtimeNS, SHA256: item.Hash, Signature: item.Signature,
				Before: item.Before, NeedsHash: item.NeedsHash, Evidence: item.Evidence,
				SourcePath: filepath.Join(s.source.RootPath, filepath.FromSlash(item.Path))}
			if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "location_id"}, {Name: "path"}}, UpdateAll: true}).Create(row).Error; err != nil {
				return err
			}
		}
		after = items[len(items)-1].ID
	}
}
