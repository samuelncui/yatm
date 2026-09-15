package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/tools"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

func (r *runner) runScope(ctx context.Context, config *Config, scope *Scope, target *entity.ReadMediaTarget) (returnErr error) {
	// Only resource acquisition and physical enumeration differ between source adapters.
	var local *locationStage
	var session mediapkg.ReadSession
	if scope.LocationID > 0 {
		var err error
		local, err = r.openLocation(ctx, scope)
		if err != nil {
			return err
		}
		defer local.release()
	} else if config.Spec.MediaId > 0 {
		var err error
		session, err = r.openSession(ctx, config, target)
		if err != nil {
			return err
		}
	}
	return r.runPipeline(ctx, config, scope, local, session)
}

func (r *runner) runPipeline(ctx context.Context, config *Config, scope *Scope, local *locationStage, session mediapkg.ReadSession) (returnErr error) {
	// A prepared source supplies enumeration and physical cleanup, not a separate processing flow.
	defer func() {
		if session != nil {
			returnErr = errors.Join(returnErr, session.Finalize(tools.WithoutTimeout(ctx)))
		}
	}()
	if local != nil {
		if err := local.enumerateInput(ctx, config); err != nil {
			return err
		}
	}
	if session != nil {
		if err := r.enumerateMedia(ctx, config, session); err != nil {
			return err
		}
	}

	// Every source uses the same content policy, comparison, derivative and validation sequence.
	r.setPhase(entity.JobPhase_JOB_PHASE_INDEXING)
	var readErr error
	if local != nil {
		readErr = local.eachScope(ctx, func(selected *Scope) error {
			if selected.Error != "" {
				return nil
			}
			if err := r.readContent(ctx, config, selected, nil); err != nil {
				return local.failScope(ctx, selected, err)
			}
			return nil
		})
	} else {
		readErr = r.readContent(ctx, config, scope, session)
	}
	checking := config.Spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES
	if readErr != nil && !checking {
		return readErr
	}
	if err := r.compareContent(ctx, config, scope); err != nil {
		return errors.Join(readErr, err)
	}
	if config.Spec.PreviewPolicy != entity.PreviewPolicy_PREVIEW_NONE {
		var previewErr error
		if local != nil {
			previewErr = local.eachScope(ctx, func(selected *Scope) error {
				if selected.Error != "" {
					return nil
				}
				if err := r.generatePreviews(ctx, config, selected); err != nil {
					return local.failScope(ctx, selected, err)
				}
				return nil
			})
		} else {
			previewErr = r.generatePreviews(ctx, config, scope)
		}
		if previewErr != nil {
			return errors.Join(readErr, previewErr)
		}
	}
	r.setPhase(entity.JobPhase_JOB_PHASE_VALIDATING_SOURCE)
	if local != nil {
		if err := local.validateEntries(ctx); err != nil {
			return err
		}
	}
	if session != nil && !checking {
		if err := r.validateInventory(ctx, session); err != nil {
			return err
		}
	}

	// Physical identity finalization gates publication of positive and negative observations alike.
	if session != nil {
		err := session.Finalize(tools.WithoutTimeout(ctx))
		session = nil
		if err != nil {
			return errors.Join(readErr, err)
		}
		if _, err := r.validateMedia(tools.WithoutTimeout(ctx), config); err != nil {
			return errors.Join(readErr, err)
		}
	}
	r.setPhase(entity.JobPhase_JOB_PHASE_PUBLISHING_SOURCE)
	if err := r.publishScope(tools.WithoutTimeout(ctx), config, scope, local); err != nil {
		return errors.Join(readErr, err)
	}
	if local != nil {
		var firstFailure error
		var incomplete int64
		if err := local.eachScope(ctx, func(selected *Scope) error {
			if selected.Error != "" {
				incomplete++
				if firstFailure == nil {
					firstFailure = fmt.Errorf("scan %q failed: %s", selected.Path, selected.Error)
				}
			}
			return nil
		}); err != nil {
			return err
		}
		if firstFailure != nil {
			return fmt.Errorf("%d Scan ranges incomplete: %w", incomplete, firstFailure)
		}
	}
	return readErr
}

func (r *runner) enumerateMedia(ctx context.Context, config *Config, session mediapkg.ReadSession) error {
	// Verification reads only its frozen baseline; missing physical paths become findings during the shared read stage.
	if config.Spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES {
		return r.db.WithContext(ctx).Model(&Entry{}).Where("published = ?", false).Updates(map[string]any{
			"change": entity.ScanChange_SCAN_CHANGE_UNCHANGED, "needs_hash": true, "finding": entity.ScanFinding_NOT_CHECKED,
			"actual_size": 0, "actual_hash": nil, "checked_at": 0, "detail": ""}).Error
	}
	physical, ok := session.(mediapkg.InventorySession)
	if !ok {
		return fmt.Errorf("Media does not provide fresh inventory enumeration")
	}
	return physical.WalkInventory(ctx, func(file *mediapkg.InventoryEntry) error {
		var row Entry
		err := r.db.WithContext(ctx).Where("location_id = 0 AND path = ?", file.Path).First(&row).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		row.Path = file.Path
		row.Size = file.Info.Size()
		row.Mode = uint32(file.Info.Mode())
		row.MtimeNs = file.Info.ModTime().UnixNano()
		placementUnchanged := bytes.Equal(row.Storage.GetOrder(), file.Storage.GetOrder()) && proto.Equal(row.Storage.GetMetadata(), file.Storage.GetMetadata())
		row.Storage = file.Storage
		row.StorageOrder = append([]byte{}, file.Storage.GetOrder()...)
		row.Change = entity.ScanChange_SCAN_CHANGE_ADDED
		if row.PositionID > 0 {
			row.Change = entity.ScanChange_SCAN_CHANGE_CHANGED
		}
		row.SourcePath, err = session.SourcePath(file.Path)
		if err != nil {
			return err
		}
		row.NeedsHash = config.Spec.SignaturePolicy != entity.ScanSignaturePolicy_KNOWN_ONLY
		// A forced read changes evidence acquisition, not whether equal content is an inventory change.
		p := row.Expected
		metadataUnchanged := p != nil && p.Size == row.Size && p.Mode == row.Mode && p.MtimeNs == row.MtimeNs
		if metadataUnchanged && placementUnchanged {
			row.Change = entity.ScanChange_SCAN_CHANGE_UNCHANGED
		}
		if config.Spec.SignaturePolicy != entity.ScanSignaturePolicy_FORCE_READ {
			if metadataUnchanged {
				row.SHA256, row.Signature = p.Sha256, p.Signature
				row.NeedsHash = len(row.SHA256) != 32 && row.NeedsHash
			}
			if session.Capabilities().Read != mediapkg.AccessSequential {
				cached, valid, err := reusableSignature(row.SourcePath)
				if err != nil {
					return err
				}
				if valid && cached.Size == row.Size && cached.MtimeNS == row.MtimeNs {
					setHash(&row, cached.SHA256[:])
				}
			}
		}
		return r.db.WithContext(ctx).Save(&row).Error
	})
}

func setHash(entry *Entry, hash []byte) {
	entry.SHA256 = append([]byte(nil), hash...)
	entry.NeedsHash = false
	entry.Signature, _ = library.NewFileSignature(hash, entry.Size)
	if before := entry.Before; before != nil && before.Size == entry.Size && bytes.Equal(before.Sha256, hash) && len(before.Signature) > 0 {
		entry.Signature = before.Signature
	}
	if expected := entry.Expected; expected != nil && expected.Size == entry.Size && bytes.Equal(expected.Sha256, hash) && len(expected.Signature) > 0 {
		entry.Signature = expected.Signature
	}
	if expected := entry.Expected; expected != nil && entry.Change == entity.ScanChange_SCAN_CHANGE_UNCHANGED && !bytes.Equal(expected.Sha256, hash) {
		entry.Change = entity.ScanChange_SCAN_CHANGE_CHANGED
	}
}

func (r *runner) scopeQuery(ctx context.Context, scope *Scope) *gorm.DB {
	query := r.db.WithContext(ctx).Where("location_id = ?", scope.LocationID)
	if scope.LocationID > 0 && scope.ID > 0 {
		query = query.Where("scope_path = ?", scope.Path)
	}
	return query
}

func (r *runner) eachEntry(ctx context.Context, scope *Scope, use func(*Entry) error) error {
	// All post-enumeration stages consume bounded manifest pages, never a filesystem-sized slice.
	var after int64
	for {
		var rows []*Entry
		if err := r.scopeQuery(ctx, scope).Where("id > ?", after).Order("id").Limit(batchSize).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, row := range rows {
			if err := use(row); err != nil {
				return err
			}
		}
		after = rows[len(rows)-1].ID
	}
}

func (r *runner) compareContent(ctx context.Context, config *Config, scope *Scope) error {
	if !config.Spec.CompareLibrary {
		return nil
	}
	return r.eachEntry(ctx, scope, func(row *Entry) error {
		if len(row.Signature) == 0 || row.Change == entity.ScanChange_SCAN_CHANGE_REMOVED {
			return nil
		}
		var count int64
		count, err := r.exe.Lib().CountSignatureCopies(ctx, row.Signature)
		if err != nil {
			return err
		}
		return r.db.WithContext(ctx).Model(row).Update("matching_copies", count).Error
	})
}

func (r *runner) generatePreviews(ctx context.Context, config *Config, scope *Scope) error {
	return r.eachEntry(ctx, scope, func(row *Entry) error {
		// Only observed, supported content with usable facts can produce derivative assets.
		if row.Change == entity.ScanChange_SCAN_CHANGE_REMOVED {
			return nil
		}
		row.Preview = entity.ScanPreviewOutcome_PREVIEW_SKIPPED
		row.PreviewError = ""
		if config.Spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES && row.Finding != entity.ScanFinding_MATCH {
			return r.db.WithContext(ctx).Save(row).Error
		}
		if len(row.SHA256) != 32 {
			row.PreviewError = "Content signature unavailable"
			return r.db.WithContext(ctx).Save(row).Error
		}
		if !r.exe.Previews().Supports(row.SourcePath) {
			return r.db.WithContext(ctx).Save(row).Error
		}
		if err := r.validatePreviewInput(ctx, config, row); err != nil {
			return err
		}

		// Content-addressed assets deduplicate work without merging independent File identities.
		var ready Entry
		if err := r.db.WithContext(ctx).Where("sha256 = ? AND size = ? AND preview = ?", row.SHA256, row.Size, entity.ScanPreviewOutcome_PREVIEW_READY).Limit(1).Find(&ready).Error; err != nil {
			return err
		}
		if ready.ID == 0 {
			_, mtime := previewObservation(config, row)
			force := config.Spec.PreviewPolicy == entity.PreviewPolicy_PREVIEW_REGENERATE_ALL
			if _, err := r.exe.Previews().Generate(ctx, row.SourcePath, row.SHA256, row.Size, mtime, force); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				row.Preview = entity.ScanPreviewOutcome_PREVIEW_FAILED
				row.PreviewError = err.Error()
				r.logger.WithError(err).WithField("path", row.Path).Warn("Scan Preview failed")
				return r.db.WithContext(ctx).Save(row).Error
			}
		}

		// Source drift invalidates the scope even if a generator successfully wrote a bundle.
		if err := r.validatePreviewInput(ctx, config, row); err != nil {
			return err
		}
		row.Preview = entity.ScanPreviewOutcome_PREVIEW_READY
		return r.db.WithContext(ctx).Save(row).Error
	})
}

func previewObservation(config *Config, row *Entry) (uint32, int64) {
	// A verified copy uses the metadata from its actual read, not historic inventory metadata.
	if config.Spec.ResultPolicy == entity.ScanResultPolicy_VERIFY_COPIES {
		return row.ReadMode, row.ReadMtimeNs
	}
	return row.Mode, row.MtimeNs
}

func (r *runner) validatePreviewInput(ctx context.Context, config *Config, row *Entry) error {
	// Cached facts do not authorize reading a relocated or replaced source without checking its current binding.
	if config.IndexedInput && row.Expected.GetFileId() > 0 {
		filename, err := r.resolveIndexedSource(ctx, row.Expected)
		if err != nil {
			return err
		}
		row.SourcePath = filename
	}
	info, err := os.Lstat(row.SourcePath)
	if err != nil {
		return err
	}
	mode, mtime := previewObservation(config, row)
	if !info.Mode().IsRegular() || info.Size() != row.Size || uint32(info.Mode()) != mode || info.ModTime().UnixNano() != mtime {
		return fmt.Errorf("Scan Preview source changed: %q", row.Path)
	}
	return nil
}

func (s *locationStage) validateEntries(ctx context.Context) error {
	// Copy actual shared-pipeline hashes back into the identity projection before complete-scope validation.
	if err := s.eachEntry(ctx, &Scope{LocationID: s.source.ID}, func(row *Entry) error {
		return s.db.WithContext(ctx).Model(&Item{}).Where("path = ?", row.Path).Updates(map[string]any{
			"hash": row.SHA256, "signature": row.Signature, "needs_hash": row.NeedsHash}).Error
	}); err != nil {
		return err
	}
	if err := s.validate(ctx, s.source); err != nil {
		return err
	}
	root, err := s.exe.CheckOnlineSource(s.source)
	if err != nil {
		return err
	}
	info, err := os.Stat(root)
	if err != nil {
		return err
	}
	if !os.SameFile(s.rootInfo, info) {
		return fmt.Errorf("Location root changed during Scan")
	}
	return nil
}

func (r *runner) validateInventory(ctx context.Context, session mediapkg.ReadSession) error {
	// A complete second enumeration detects additions as well as removals and changed ordinary files.
	physical, ok := session.(mediapkg.InventorySession)
	if !ok {
		return fmt.Errorf("Media inventory validation is unavailable")
	}
	var seen int64
	if err := physical.WalkInventory(ctx, func(file *mediapkg.InventoryEntry) error {
		var row Entry
		if err := r.db.WithContext(ctx).Where("location_id = 0 AND path = ? AND change != ?", file.Path, entity.ScanChange_SCAN_CHANGE_REMOVED).First(&row).Error; err != nil {
			return fmt.Errorf("Media membership changed: %q, %w", file.Path, err)
		}
		if row.Size != file.Info.Size() || row.Mode != uint32(file.Info.Mode()) || row.MtimeNs != file.Info.ModTime().UnixNano() {
			return fmt.Errorf("Media facts changed: %q", file.Path)
		}
		seen++
		return nil
	}); err != nil {
		return err
	}
	var expected int64
	if err := r.db.WithContext(ctx).Model(&Entry{}).Where("location_id = 0 AND change != ?", entity.ScanChange_SCAN_CHANGE_REMOVED).Count(&expected).Error; err != nil {
		return err
	}
	if expected != seen {
		return fmt.Errorf("Media membership changed: observed=%d expected=%d", seen, expected)
	}
	return nil
}

func (r *runner) publishScope(ctx context.Context, config *Config, scope *Scope, local *locationStage) error {
	// Result policy changes only publication; all observations and content work already passed the shared stages.
	switch config.Spec.ResultPolicy {
	case entity.ScanResultPolicy_PUBLISH_ORIGINALS:
		if local == nil {
			return fmt.Errorf("original publication requires a Location")
		}
		if err := local.publish(ctx, scope); err != nil {
			return err
		}
	case entity.ScanResultPolicy_PUBLISH_INVENTORY:
		if _, err := r.exe.Lib().ApplyScan(ctx, config.Spec.MediaId, func(ctx context.Context, yield func(*entity.ScanEntry) error) error {
			var after string
			for {
				var entries []*Entry
				if err := r.db.WithContext(ctx).Where("location_id = 0 AND path > ? AND change != ?", after, entity.ScanChange_SCAN_CHANGE_UNCHANGED).Order("path").Limit(batchSize).Find(&entries).Error; err != nil {
					return err
				}
				if len(entries) == 0 {
					return nil
				}
				for _, entry := range entries {
					if err := yield(entry.ToEntity()); err != nil {
						return err
					}
				}
				after = entries[len(entries)-1].Path
			}
		}); err != nil {
			return err
		}
	case entity.ScanResultPolicy_VERIFY_COPIES:
		if err := r.publishChecks(ctx, scope); err != nil {
			return err
		}
	}
	if config.Spec.ResultPolicy != entity.ScanResultPolicy_REPORT_ONLY {
		scope.PublishedAt = time.Now().UnixMilli()
	}
	return r.db.WithContext(ctx).Model(&Scope{}).Where("location_id = ? AND error = ?", scope.LocationID, "").Update("published_at", scope.PublishedAt).Error
}

func (r *runner) publishChecks(ctx context.Context, scope *Scope) error {
	return r.eachEntry(ctx, scope, func(row *Entry) error {
		if row.Published || row.Finding == entity.ScanFinding_NOT_CHECKED {
			return nil
		}
		health := entity.PositionHealth_POSITION_HEALTH_UNKNOWN
		switch row.Finding {
		case entity.ScanFinding_MATCH:
			health = entity.PositionHealth_HEALTHY
		case entity.ScanFinding_MISMATCH:
			health = entity.PositionHealth_DAMAGED
		case entity.ScanFinding_MISSING:
			health = entity.PositionHealth_MISSING
		case entity.ScanFinding_UNREADABLE:
			health = entity.PositionHealth_UNREADABLE
		}
		if health != entity.PositionHealth_POSITION_HEALTH_UNKNOWN {
			published, err := r.exe.Lib().PublishPositionHealth(ctx, &library.PositionHealthObservation{PositionID: row.PositionID,
				ContentToken: row.ContentToken, Health: health, CheckedAt: row.CheckedAt, JobID: r.job.ID})
			if err != nil {
				return err
			}
			row.Stale = !published
		}
		row.Published = true
		return r.db.WithContext(ctx).Save(row).Error
	})
}
