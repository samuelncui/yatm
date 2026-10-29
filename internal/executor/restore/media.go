package restore

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"google.golang.org/protobuf/proto"
)

func (a *jobRestoreRunner) restoreMedia(ctx context.Context, target *entity.ReadMediaTarget) (returnErr error) {
	// Preserve the frozen FileVersion and destination identity through the complete transfer attempt.
	var pending int64
	if err := a.db.WithContext(ctx).Model(&Copy{}).Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).Count(&pending).Error; err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}

	// Prepare and validate the selected physical Media before entering the attempt.
	if _, err := a.exe.RestoreOutputPath(ctx, a.destination, ""); err != nil {
		return err
	}
	names, err := newOutputNames(ctx, a)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, names.close()) }()
	if err := names.rebuild(ctx); err != nil {
		return err
	}
	target, err = a.frozenReadTarget(ctx, target)
	if err != nil {
		return err
	}
	session, err := a.exe.NewMediaBackend(a.job.ID, a.logger, a.mediaWait).NewReadSession(ctx, target)
	if err != nil {
		return err
	}
	// Every return after acquisition releases physical resources, after any result writer drains.
	finalized := false
	defer func() {
		if !finalized {
			returnErr = errors.Join(returnErr, session.Finalize(context.WithoutCancel(ctx), returnErr))
		}
	}()
	selected := session.Media()
	if selected == nil || selected.ID == 0 {
		return fmt.Errorf("Media read session has no selected Media")
	}
	if err := a.db.WithContext(ctx).Model(&Copy{}).
		Where("media_id = ? AND status = ?", selected.ID, entity.CopyStatus_COPY_STATUS_PENDING).Count(&pending).Error; err != nil {
		return err
	}
	if pending == 0 {
		return fmt.Errorf("Restore Media has no pending items")
	}
	if err := a.db.WithContext(ctx).Model(&Copy{}).Where("media_id = ? AND health_published = ?", selected.ID, false).
		Update("health_checked_at_ns", 0).Error; err != nil {
		return err
	}

	// Keep progress scoped to this complete Media attempt.
	progress := a.getProgress()
	defer func() {
		if returnErr != nil {
			a.dropProgress()
		}
	}()
	if err := a.transition(restoreStateCopying); err != nil {
		return err
	}
	progress.StartSession()
	// The attempt identity is fixed here and shared by the sampling work path and the request path.
	a.lock.Lock()
	a.stageKey = progress.StagePrefix()
	a.lock.Unlock()

	// Run the shared transfer stream with capabilities owned by the read Session. The runner
	// owns the items, the callback only hands finished ones to the writer, and the shared writer
	// completes and stages each batch outside the feed.
	settings, err := a.executionSettings(ctx)
	if err != nil {
		return err
	}
	buffer, err := newCopyBuffer(ctx, a)
	if err != nil {
		return err
	}
	// The writer is drained on every return path; Close is idempotent, so the run path below reads
	// its terminal error again.
	defer func() { _ = buffer.Close() }()
	source := &copyItems{runner: a, mediaID: selected.ID, session: session, batch: settings.BatchSize}

	engine, err := acp.NewStream(
		ctx,
		buffer.onResults,
		acp.WithHashPolicy(acp.HashReadRefresh),
		// A Restore never replaces an existing output, which stays a run-level option.
		acp.Overwrite(false),
		acp.SetFromDevice(mediapkg.DeviceOptions(session.Capabilities().Read)...),
		acp.WithReadBuffer(settings.ReadBufferMax),
		// ACP owns the result queue and the batch; the shared writer only persists what it hands over.
		acp.WithResultBuffer(settings.ResultBufferMax),
		acp.WithResultBatch(settings.ResultBatchSize),
		acp.WithResultFlushInterval(settings.ResultFlushInterval),
		acp.WithLogger(a.logger),
		acp.WithEventHandler(a.restoreEventHandler(ctx)),
	)
	if err != nil {
		return err
	}
	runErr := source.consume(ctx, engine)

	// Drain accepted results before combining transfer and checkpoint failures.
	// An earlier item failure must not hide a later persistence error.
	writeErr := buffer.Close()
	copyErr := errors.Join(runErr, writeErr)
	if err := a.transition(restoreStateFinalizingMedia); err != nil {
		copyErr = errors.Join(copyErr, err)
	}

	// Finalize physical resources before committing the successful progress checkpoint.
	finalized = true
	finalizeErr := session.Finalize(context.WithoutCancel(ctx), copyErr)
	if finalizeErr != nil {
		return errors.Join(copyErr, finalizeErr)
	}
	healthErr := a.publishHealth(context.WithoutCancel(ctx), selected.ID)
	publicationErr := a.finalizeOutputs(context.WithoutCancel(ctx), selected.ID)
	if copyErr != nil || healthErr != nil || publicationErr != nil {
		return errors.Join(copyErr, healthErr, publicationErr)
	}
	progress.CommitSession()
	return nil
}

func (a *jobRestoreRunner) frozenReadTarget(ctx context.Context, target *entity.ReadMediaTarget) (*entity.ReadMediaTarget, error) {
	// Resolve only the physical selector; expectations always come from frozen pending candidates.
	if target == nil {
		return nil, fmt.Errorf("Restore Media target is missing")
	}
	selected := &entity.ReadMediaTarget{}
	var copy Copy
	switch backend := target.Backend.(type) {
	case *entity.ReadMediaTarget_Volume:
		identity, err := mediapkg.NormalizeVolumeUUID(backend.Volume.GetUuid())
		if err != nil {
			return nil, err
		}
		if err := a.db.WithContext(ctx).Where("media_identity = ? AND status = ?", identity, entity.CopyStatus_COPY_STATUS_PENDING).Order("id").First(&copy).Error; err != nil {
			return nil, fmt.Errorf("Media %q has no frozen pending Restore candidate, %w", identity, err)
		}
		if copy.MediaProfile.GetVolume() == nil {
			return nil, fmt.Errorf("Restore candidate is not Volume Media")
		}
		selected.Backend = &entity.ReadMediaTarget_Volume{Volume: &entity.ReadVolumeTarget{Uuid: identity}}
	case *entity.ReadMediaTarget_Tape:
		if backend.Tape == nil {
			return nil, fmt.Errorf("Restore Tape target is missing")
		}
		tape := proto.Clone(backend.Tape).(*entity.ReadTapeTarget)
		tape.Device = strings.TrimSpace(tape.Device)
		selected.Backend = &entity.ReadMediaTarget_Tape{Tape: tape}
		if tape.MediaId != nil {
			if tape.GetMediaId() <= 0 {
				return nil, fmt.Errorf("Restore Tape Media hint is invalid")
			}
			if err := a.db.WithContext(ctx).Where("media_id = ? AND status = ?", tape.GetMediaId(), entity.CopyStatus_COPY_STATUS_PENDING).Order("id").First(&copy).Error; err != nil {
				return nil, fmt.Errorf("Media %d has no frozen pending Restore candidate, %w", tape.GetMediaId(), err)
			}
			if copy.MediaProfile.GetTape() == nil {
				return nil, fmt.Errorf("Restore candidate is not Tape Media")
			}
		}

		// Inspect frozen profile families without materializing the candidate set.
		raw := false
		if tape.MediaId == nil {
			var err error
			raw, err = a.hasPendingTarMedia(ctx)
			if err != nil {
				return nil, err
			}
		}
		if tape.RefreshPositionMap && !raw && copy.MediaProfile.GetTape().GetFormat() != library.TapeFormatTarPAXV1 {
			return nil, fmt.Errorf("position-map refresh requires tar Restore Media")
		}

		// Resolve physical identity only while owning the drive; raw tar hints bypass LTFS scripts.
		err := a.exe.AcquireTapeDevice(ctx, a.job.ID, tape.Device, func() { a.mediaWait(true) })
		a.mediaWait(false)
		if err != nil {
			return nil, err
		}
		if raw {
			resolved, err := a.exe.ResolveTapeReadMedia(ctx, a.job.ID, tape,
				func(ctx context.Context, identity string) (*entity.ReadMediaTarget, error) {
					return a.selectFrozenTapeMedia(ctx, tape, identity)
				})
			if err != nil {
				return nil, err
			}
			if resolved == nil {
				return nil, fmt.Errorf("Restore Tape identification returned no Media")
			}
			return resolved, nil
		}

		if tape.MediaId != nil && copy.MediaProfile.GetTape().Format == library.TapeFormatTarPAXV1 {
			break
		}
		identity, err := a.exe.ReadTapeBarcode(ctx, tape.Device)
		if err != nil {
			return nil, err
		}
		if identity == "" {
			return nil, fmt.Errorf("Restore Media identity is unavailable")
		}
		if tape.MediaId != nil && identity != copy.MediaIdentity {
			return nil, fmt.Errorf("Restore Tape identity differs from selected Media")
		}
		if tape.MediaId == nil {
			if err := a.db.WithContext(ctx).Where("media_identity = ? AND status = ?", identity, entity.CopyStatus_COPY_STATUS_PENDING).Order("id").First(&copy).Error; err != nil {
				return nil, fmt.Errorf("Media %q has no frozen pending Restore candidate, %w", identity, err)
			}
			if copy.MediaProfile.GetTape() == nil {
				return nil, fmt.Errorf("Restore candidate is not Tape Media")
			}
		}
	default:
		return nil, fmt.Errorf("Restore requires a Tape or Volume")
	}

	// Rebuild trusted expectations instead of allowing the caller to substitute a cartridge profile.
	if copy.MediaProfile == nil {
		return nil, fmt.Errorf("Restore candidate Media profile is missing")
	}
	selected.ExpectedMediaId, selected.ExpectedIdentity = copy.MediaID, copy.MediaIdentity
	selected.ExpectedProfile = proto.Clone(copy.MediaProfile).(*entity.MediaProfile)
	return selected, nil
}

// hasPendingTarMedia examines one frozen profile at a time before choosing the identification path.
func (a *jobRestoreRunner) hasPendingTarMedia(ctx context.Context) (bool, error) {
	// Stream through the existing pending index; no profile list survives the query.
	rows, err := a.db.WithContext(ctx).Model(&Copy{}).Select("media_profile").
		Where("status = ?", entity.CopyStatus_COPY_STATUS_PENDING).Rows()
	if err != nil {
		return false, fmt.Errorf("read pending Restore Media profiles failed, %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var copy Copy
		if err := a.db.ScanRows(rows, &copy); err != nil {
			return false, fmt.Errorf("decode pending Restore Media profile failed, %w", err)
		}
		if copy.MediaProfile.GetTape().GetFormat() == library.TapeFormatTarPAXV1 {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("read pending Restore Media profiles failed, %w", err)
	}
	return false, nil
}

// selectFrozenTapeMedia resolves a cartridge memory barcode only against this Job's pending cartridge facts.
func (a *jobRestoreRunner) selectFrozenTapeMedia(
	ctx context.Context, requested *entity.ReadTapeTarget, identity string,
) (*entity.ReadMediaTarget, error) {
	// Inspect matching frozen rows one at a time; the barcode alone must select one Media.
	identity = strings.ToUpper(strings.TrimSpace(identity))
	if identity == "" {
		return nil, fmt.Errorf("Restore Media identity is unavailable")
	}
	rows, err := a.db.WithContext(ctx).Model(&Copy{}).Select("media_id", "media_identity", "media_profile").
		Where("media_identity = ? AND status = ?", identity, entity.CopyStatus_COPY_STATUS_PENDING).
		Group("media_id").Order("media_id").Rows()
	if err != nil {
		return nil, fmt.Errorf("read frozen Restore Media failed, %w", err)
	}
	defer rows.Close()
	var selected *Copy
	for rows.Next() {
		var copy Copy
		if err := a.db.ScanRows(rows, &copy); err != nil {
			return nil, fmt.Errorf("decode frozen Restore Media failed, %w", err)
		}
		tape := copy.MediaProfile.GetTape()
		if tape == nil {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("Media %q has ambiguous frozen pending Restore candidates", identity)
		}
		selected = &copy
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read frozen Restore Media failed, %w", err)
	}
	if selected == nil {
		return nil, fmt.Errorf("Media %q has no frozen pending Restore candidate", identity)
	}
	if requested.RefreshPositionMap && selected.MediaProfile.GetTape().GetFormat() != library.TapeFormatTarPAXV1 {
		return nil, fmt.Errorf("position-map refresh requires tar Restore Media")
	}

	// Keep the operator's device/action choices while rebuilding every trusted expectation.
	selector := proto.Clone(requested).(*entity.ReadTapeTarget)
	selector.Device = strings.TrimSpace(selector.Device)
	selector.MediaId = proto.Int64(selected.MediaID)
	target := selector.Pack()
	target.ExpectedMediaId, target.ExpectedIdentity = selected.MediaID, selected.MediaIdentity
	target.ExpectedProfile = proto.Clone(selected.MediaProfile).(*entity.MediaProfile)
	return target, nil
}

func (a *jobRestoreRunner) publishHealth(ctx context.Context, mediaID int64) error {
	// A finalized read session makes its bounded per-copy observations eligible for inventory publication.
	var after int64
	for {
		var copies []Copy
		if err := a.db.WithContext(ctx).Where("media_id = ? AND id > ? AND health_checked_at_ns > 0 AND health_published = ?", mediaID, after, false).
			Order("id").Limit(batchSize).Find(&copies).Error; err != nil {
			return err
		}
		if len(copies) == 0 {
			return nil
		}
		for _, copy := range copies {
			if copy.PositionID != 0 {
				if err := a.exe.Lib().PublishPositionHealth(ctx, &library.PositionHealthObservation{
					PositionID: copy.PositionID, Health: copy.Health,
					CheckedAtNS: copy.HealthCheckedAtNS, JobID: a.job.ID,
				}); err != nil {
					return err
				}
			}
			if err := a.db.WithContext(ctx).Model(&Copy{}).Where("id = ?", copy.ID).Update("health_published", true).Error; err != nil {
				return err
			}
			after = copy.ID
		}
	}
}
