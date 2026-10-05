package library

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestPositionHealthPublishesCompletedObservations(t *testing.T) {
	// Store one opaque-signature inventory entry without manufacturing a healthy observation.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	hash := sha256.Sum256([]byte("content"))
	position := &Position{MediaID: 1, Path: "file", Signature: []byte("opaque"), Hash: hash[:], Size: 7, Mode: 0644}
	require.NoError(t, lib.SavePosition(ctx, position))
	observation := &PositionHealthObservation{PositionID: position.ID, Health: entity.PositionHealth_POSITION_HEALTH_HEALTHY, CheckedAtNS: 100, JobID: 2}

	// The completed result changes health without changing its content baseline.
	require.NoError(t, lib.PublishPositionHealth(ctx, observation))
	observation.Health, observation.CheckedAtNS = entity.PositionHealth_POSITION_HEALTH_DAMAGED, 200
	require.NoError(t, lib.PublishPositionHealth(ctx, observation))
	stored, err := lib.GetPosition(ctx, position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_DAMAGED, stored.Health)
	require.Equal(t, hash[:], stored.Hash)
	require.Equal(t, []byte("opaque"), stored.Signature)

	// Changed expected content clears the old health rather than transferring it to another content identity.
	stored.Hash = make([]byte, sha256.Size)
	require.NoError(t, lib.SavePosition(ctx, stored))
	stored, err = lib.GetPosition(ctx, position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
	require.Zero(t, stored.CheckedAtNS)
}

func TestPositionHealthConcurrentIndependentPublications(t *testing.T) {
	// A concurrent-capable GORM handle lets independent checks reach write callbacks together.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	const checks = 8
	positions := make([]*Position, checks)
	for i := range positions {
		positions[i] = &Position{MediaID: 1, Path: fmt.Sprintf("file-%d", i)}
		require.NoError(t, lib.SavePosition(ctx, positions[i]))
	}
	lib = New(db.Session(&gorm.Session{SkipDefaultTransaction: true}))
	publicationCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	arrived := make(chan struct{}, checks)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	require.NoError(t, db.Callback().Update().Before("gorm:before_update").Register("test:position_health_concurrent", func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "positions" {
			arrived <- struct{}{}
			<-release
		}
	}))

	// Release all updates together, then check each independently published result.
	errors := make(chan error, checks)
	for _, position := range positions {
		go func() {
			errors <- lib.PublishPositionHealth(publicationCtx, &PositionHealthObservation{
				PositionID: position.ID, Health: entity.PositionHealth_POSITION_HEALTH_HEALTHY,
				CheckedAtNS: 100, JobID: 2,
			})
		}()
	}
	for range positions {
		select {
		case <-arrived:
		case <-publicationCtx.Done():
			t.Fatal("concurrent Position updates did not reach the GORM callback")
		}
	}
	close(release)
	released = true
	for range positions {
		select {
		case err := <-errors:
			require.NoError(t, err)
		case <-publicationCtx.Done():
			t.Fatal("concurrent Position updates did not finish")
		}
	}
	for _, position := range positions {
		stored, err := lib.GetPosition(ctx, position.ID)
		require.NoError(t, err)
		require.Equal(t, entity.PositionHealth_POSITION_HEALTH_HEALTHY, stored.Health)
	}
}

func TestSavePositionWithExplicitNewIDPreservesUpsertSemantics(t *testing.T) {
	// Import callers can establish a previously absent integer ID without claiming a physical check.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	position := &Position{ID: 42, MediaID: 1, Path: "file", Size: 7, Hash: make([]byte, sha256.Size)}
	require.NoError(t, lib.SavePosition(ctx, position))
	stored, err := lib.GetPosition(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)

	// The same ID remains an ordinary inventory update after insertion.
	stored.Mode = 0600
	require.NoError(t, lib.SavePosition(ctx, stored))
	stored, err = lib.GetPosition(ctx, 42)
	require.NoError(t, err)
	require.EqualValues(t, 0600, stored.Mode)
}

func TestScanDoesNotClearKnownDamageForUnchangedContent(t *testing.T) {
	// Store a checked damaged copy whose metadata will be refreshed by inventory Scan.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	media, err := lib.CreateMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()})
	require.NoError(t, err)
	hash := sha256.Sum256([]byte("saved"))
	signature, err := NewFileSignature(hash[:], 5)
	require.NoError(t, err)
	position := &Position{MediaID: media.ID, Path: "file", Hash: hash[:], Signature: signature, Size: 5, Mode: 0644,
		MtimeNS: time.Unix(1, 0).UnixNano(), WrittenAtNS: time.Unix(1, 0).UnixNano()}
	require.NoError(t, lib.SavePosition(ctx, position))
	require.NoError(t, lib.PublishPositionHealth(ctx, &PositionHealthObservation{PositionID: position.ID,
		Health: entity.PositionHealth_POSITION_HEALTH_DAMAGED, CheckedAtNS: 100, JobID: 2}))

	// An identical hash/size with changed mtime retains the bad check; Scan is not integrity verification.
	_, err = lib.ApplyScan(ctx, media.ID, func(_ context.Context, yield func(*entity.ScanEntry) error) error {
		return yield(&entity.ScanEntry{Path: "file", Change: entity.ScanChange_SCAN_CHANGE_CHANGED,
			Mode: 0644, MtimeNs: time.Unix(2, 0).UnixNano(), SizeBytes: 5, Sha256: hash[:]})
	})
	require.NoError(t, err)
	stored, err := lib.GetPosition(ctx, position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_DAMAGED, stored.Health)
	require.Equal(t, int64(100), stored.CheckedAtNS)
}

func TestPositionHealthRejectsMissingDirectoryAndInvalidObservations(t *testing.T) {
	// A health observation belongs to an existing regular Position, never a derived directory.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	directory := &Position{MediaID: 1, Path: "folder/", IsDir: true}
	require.NoError(t, lib.SavePosition(ctx, directory))
	observation := &PositionHealthObservation{PositionID: directory.ID, Health: entity.PositionHealth_POSITION_HEALTH_HEALTHY, CheckedAtNS: 10}
	require.ErrorContains(t, lib.PublishPositionHealth(ctx, observation), "regular Position")
	observation.PositionID++
	require.ErrorContains(t, lib.PublishPositionHealth(ctx, observation), "regular Position")

	// Unknown is the absence of a check and cannot overwrite a completed observation through this API.
	observation.Health = entity.PositionHealth_POSITION_HEALTH_UNKNOWN
	require.Error(t, lib.PublishPositionHealth(ctx, observation))
	require.True(t, PositionRestoreEligible(entity.PositionHealth_POSITION_HEALTH_UNKNOWN))
	require.True(t, PositionRestoreEligible(entity.PositionHealth_POSITION_HEALTH_HEALTHY))
	for _, health := range []entity.PositionHealth{entity.PositionHealth_POSITION_HEALTH_DAMAGED, entity.PositionHealth_POSITION_HEALTH_MISSING, entity.PositionHealth_POSITION_HEALTH_UNREADABLE} {
		require.False(t, PositionRestoreEligible(health))
	}
}

func TestCommitMediaHealthRequiresExplicitPhysicalVerification(t *testing.T) {
	for _, checkedAt := range []int64{0, 1234} {
		t.Run(fmt.Sprint(checkedAt), func(t *testing.T) {
			// Generic inventory publication does not imply an actual byte verification occurred.
			ctx := context.Background()
			_, lib := newTestLibrary(t)
			hash := sha256.Sum256([]byte("content"))
			media, err := lib.CommitMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
				Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()},
				func(_ context.Context, yield func(*MediaFile) error) error {
					return yield(&MediaFile{Path: "file", Hash: hash[:], Size: 7, CheckedAtNS: checkedAt, HealthJobID: 42})
				})
			require.NoError(t, err)

			// Only explicit post-Finalize evidence starts the Position as known healthy.
			positions, err := lib.ListMediaFilePositions(ctx, media.ID, "", 10)
			require.NoError(t, err)
			require.Len(t, positions, 1)
			want := entity.PositionHealth_POSITION_HEALTH_UNKNOWN
			if checkedAt > 0 {
				want = entity.PositionHealth_POSITION_HEALTH_HEALTHY
				require.Equal(t, int64(42), positions[0].HealthJobID)
			}
			require.Equal(t, want, positions[0].Health)
			require.Equal(t, checkedAt, positions[0].CheckedAtNS)
		})
	}
}
