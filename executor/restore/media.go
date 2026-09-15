package restore

import (
	"context"
	"errors"
	"fmt"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/tools"
)

func (a *jobRestoreRunner) restoreMedia(ctx context.Context, target *entity.ReadMediaTarget) (returnErr error) {
	// Preserve the frozen FileVersion and destination identity through the complete transfer attempt.
	release, err := a.destinationAdmission()
	if err != nil {
		return err
	}
	defer release()
	if err := a.reconcileResults(ctx); err != nil {
		return err
	}
	if err := a.publishReadyOutputs(ctx, true); err != nil {
		return err
	}
	var pending int64
	if err := a.db.WithContext(ctx).Model(&Copy{}).Where("status = ?", entity.CopyStatus_PENDING).Count(&pending).Error; err != nil {
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
	session, err := a.exe.NewMediaBackend(a.job.ID, a.logger).NewReadSession(ctx, a.db, target)
	if err != nil {
		return err
	}
	selected := session.Inspect()
	if selected == nil || selected.ID == 0 {
		finalizeErr := session.Finalize(tools.WithoutTimeout(ctx))
		return errors.Join(fmt.Errorf("Media read session has no selected Media"), finalizeErr)
	}
	if err := a.db.WithContext(ctx).Model(&Copy{}).
		Where("media_id = ? AND status = ?", selected.ID, entity.CopyStatus_PENDING).Count(&pending).Error; err != nil {
		return errors.Join(err, session.Finalize(tools.WithoutTimeout(ctx)))
	}
	if pending == 0 {
		return errors.Join(fmt.Errorf("Restore Media has no pending items"), session.Finalize(tools.WithoutTimeout(ctx)))
	}
	if err := a.db.WithContext(ctx).Model(&Copy{}).Where("media_id = ? AND health_published = ?", selected.ID, false).
		Update("health_checked_at", 0).Error; err != nil {
		return errors.Join(err, session.Finalize(tools.WithoutTimeout(ctx)))
	}

	// Keep retryable progress scoped to this complete Media attempt.
	progress := a.getProgress()
	defer func() {
		if returnErr != nil {
			a.dropProgress()
		}
	}()
	if err := a.transition(restoreStateCopying); err != nil {
		finalizeErr := session.Finalize(tools.WithoutTimeout(ctx))
		return errors.Join(err, finalizeErr)
	}
	progress.StartSession()

	// Run the shared transfer stream with capabilities owned by the read Session.
	copyErr := acp.RunStream(
		ctx,
		&copySource{runner: a, mediaID: selected.ID, session: session},
		&copySink{runner: a, mediaID: selected.ID, session: session},
		acp.WithHash(true),
		acp.WithSignatureCache(true),
		acp.Overwrite(false),
		acp.SetFromDevice(mediapkg.DeviceOptions(session.Capabilities().Read)...),
		acp.WithLogger(a.logger),
		acp.WithEventHandler(a.restoreEventHandler(ctx)),
	)
	if err := a.transition(restoreStateFinalizingMedia); err != nil {
		copyErr = errors.Join(copyErr, err)
	}

	// Finalize physical resources before committing the successful progress checkpoint.
	finalizeErr := session.Finalize(tools.WithoutTimeout(ctx))
	if finalizeErr != nil {
		return errors.Join(copyErr, finalizeErr)
	}
	healthErr := a.publishHealth(tools.WithoutTimeout(ctx), selected.ID)
	publicationErr := a.finalizeOutputs(tools.WithoutTimeout(ctx), selected.ID)
	if copyErr != nil || healthErr != nil || publicationErr != nil {
		return errors.Join(copyErr, healthErr, publicationErr)
	}
	progress.CommitSession()
	return nil
}

func (a *jobRestoreRunner) frozenReadTarget(ctx context.Context, target *entity.ReadMediaTarget) (*entity.ReadMediaTarget, error) {
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
		value, err := a.exe.ReadTapeBarcode(ctx, backend.Tape.GetDevice())
		if err != nil {
			return nil, err
		}
		identity = value
		selected.Backend = &entity.ReadMediaTarget_Tape{Tape: &entity.ReadTapeTarget{Device: backend.Tape.GetDevice()}}
	default:
		return nil, fmt.Errorf("Restore requires a Tape or Volume")
	}
	if identity == "" {
		return nil, fmt.Errorf("Restore Media identity is unavailable")
	}

	// Reject a different catalog incarnation before encryption, mounting or reading any content.
	var copy Copy
	if err := a.db.WithContext(ctx).Where("media_identity = ? AND status = ?", identity, entity.CopyStatus_PENDING).
		Order("id").First(&copy).Error; err != nil {
		return nil, fmt.Errorf("Media %q has no frozen pending Restore candidate, %w", identity, err)
	}
	if copy.MediaProfile == nil {
		return nil, fmt.Errorf("Restore candidate Media profile is missing")
	}
	selected.ExpectedMediaId, selected.ExpectedIdentity, selected.ExpectedProfile = copy.MediaID, copy.MediaIdentity, copy.MediaProfile
	return selected, nil
}

func (a *jobRestoreRunner) publishHealth(ctx context.Context, mediaID int64) error {
	// A finalized read session makes its bounded per-copy observations eligible for inventory publication.
	var after int64
	for {
		var copies []Copy
		if err := a.db.WithContext(ctx).Where("media_id = ? AND id > ? AND health_checked_at > 0 AND health_published = ?", mediaID, after, false).
			Order("id").Limit(batchSize).Find(&copies).Error; err != nil {
			return err
		}
		if len(copies) == 0 {
			return nil
		}
		for _, copy := range copies {
			if copy.PositionID != 0 {
				if _, err := a.exe.Lib().PublishPositionHealth(ctx, &library.PositionHealthObservation{
					PositionID: copy.PositionID, ContentToken: copy.PositionContentToken, Health: copy.Health,
					CheckedAt: copy.HealthCheckedAt, JobID: a.job.ID,
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
