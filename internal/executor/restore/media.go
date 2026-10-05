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
			returnErr = errors.Join(returnErr, session.Finalize(context.WithoutCancel(ctx)))
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
	buffer, err := newCopyBuffer(ctx, a, selected.ID, session)
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

	// The transfer attempt's own failures are per-item outcomes; only a pipeline failure or a
	// failed persistence makes the run fail, and both are reported after the results are persisted.
	writeErr := buffer.Close()
	copyErr := runErr
	if copyErr == nil {
		copyErr = writeErr
	}
	if err := a.transition(restoreStateFinalizingMedia); err != nil {
		copyErr = errors.Join(copyErr, err)
	}

	// Finalize physical resources before committing the successful progress checkpoint.
	finalized = true
	finalizeErr := session.Finalize(context.WithoutCancel(ctx))
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

func (a *jobRestoreRunner) frozenReadTarget(
	ctx context.Context, target *entity.ReadMediaTarget,
) (*entity.ReadMediaTarget, error) {
	// Resolve only the physical selector; expectations always come from this Job's frozen candidates.
	selected := &entity.ReadMediaTarget{}
	var identity string
	switch backend := target.Backend.(type) {
	case *entity.ReadMediaTarget_Volume:
		value, err := mediapkg.NormalizeVolumeUUID(backend.Volume.GetUuid())
		if err != nil {
			return nil, err
		}
		identity = value
		selected.Backend = &entity.ReadMediaTarget_Volume{Volume: &entity.ReadVolumeTarget{Uuid: value}}
	case *entity.ReadMediaTarget_Tape:
		// Resolve the cartridge only after owning the drive; Session setup reuses this attempt lease.
		device := strings.TrimSpace(backend.Tape.GetDevice())
		err := a.exe.AcquireTapeDevice(ctx, a.job.ID, device, func() { a.mediaWait(true) })
		a.mediaWait(false)
		if err != nil {
			return nil, err
		}
		value, err := a.exe.ReadTapeBarcode(ctx, device)
		if err != nil {
			return nil, err
		}
		identity = value
		selected.Backend = &entity.ReadMediaTarget_Tape{Tape: &entity.ReadTapeTarget{Device: device}}
	default:
		return nil, fmt.Errorf("Restore requires a Tape or Volume")
	}
	if identity == "" {
		return nil, fmt.Errorf("Restore Media identity is unavailable")
	}

	// Reject a different catalog incarnation before encryption, mounting or reading any content.
	var copy Copy
	if err := a.db.WithContext(ctx).
		Where("media_identity = ? AND status = ?", identity, entity.CopyStatus_COPY_STATUS_PENDING).
		Order("id").First(&copy).Error; err != nil {
		return nil, fmt.Errorf("Media %q has no frozen pending Restore candidate, %w", identity, err)
	}
	if copy.MediaProfile == nil {
		return nil, fmt.Errorf("Restore candidate Media profile is missing")
	}
	selected.ExpectedMediaId, selected.ExpectedIdentity = copy.MediaID, copy.MediaIdentity
	selected.ExpectedProfile = copy.MediaProfile
	return selected, nil
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
