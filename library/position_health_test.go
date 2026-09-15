package library

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/stretchr/testify/require"
)

func TestPositionHealthPublishesOnlyFrozenFacts(t *testing.T) {
	// Freeze one opaque-signature inventory entry without manufacturing a healthy observation.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	hash := sha256.Sum256([]byte("content"))
	position := &Position{MediaID: 1, Path: "file", Signature: []byte("opaque"), Hash: hash[:], Size: 7, Mode: 0644}
	require.NoError(t, lib.SavePosition(ctx, position))
	token, err := PositionContentToken(position)
	require.NoError(t, err)
	observation := &PositionHealthObservation{PositionID: position.ID, ContentToken: token, Health: entity.PositionHealth_HEALTHY, CheckedAt: 100, JobID: 2}

	// A completed check is replayable, while a later check cannot be overwritten by an older observation.
	for attempt := 0; attempt < 2; attempt++ {
		published, err := lib.PublishPositionHealth(ctx, observation)
		require.NoError(t, err)
		require.True(t, published)
	}
	observation.Health, observation.CheckedAt = entity.PositionHealth_DAMAGED, 200
	published, err := lib.PublishPositionHealth(ctx, observation)
	require.NoError(t, err)
	require.True(t, published)
	observation.Health, observation.CheckedAt = entity.PositionHealth_HEALTHY, 100
	published, err = lib.PublishPositionHealth(ctx, observation)
	require.NoError(t, err)
	require.False(t, published)
	stored, err := lib.GetPosition(ctx, position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_DAMAGED, stored.Health)
	require.Equal(t, hash[:], stored.Hash)
	require.Equal(t, []byte("opaque"), stored.Signature)

	// A metadata replacement invalidates outstanding observations without inventing a new healthy result.
	stored.Mode = 0600
	require.NoError(t, lib.SavePosition(ctx, stored))
	observation.CheckedAt = 300
	published, err = lib.PublishPositionHealth(ctx, observation)
	require.NoError(t, err)
	require.False(t, published)
	stored, err = lib.GetPosition(ctx, position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_DAMAGED, stored.Health)

	// Changed expected content clears the old health rather than transferring it to another content identity.
	stored.Hash = make([]byte, sha256.Size)
	require.NoError(t, lib.SavePosition(ctx, stored))
	stored, err = lib.GetPosition(ctx, position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
	require.Zero(t, stored.CheckedAt)
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
		ModTime: time.Unix(1, 0), WriteTime: time.Unix(1, 0)}
	require.NoError(t, lib.SavePosition(ctx, position))
	token, err := PositionContentToken(position)
	require.NoError(t, err)
	published, err := lib.PublishPositionHealth(ctx, &PositionHealthObservation{PositionID: position.ID, ContentToken: token,
		Health: entity.PositionHealth_DAMAGED, CheckedAt: 100, JobID: 2})
	require.NoError(t, err)
	require.True(t, published)

	// An identical hash/size with changed mtime retains the bad check; Scan is not integrity verification.
	_, err = lib.ApplyScan(ctx, media.ID, func(_ context.Context, yield func(*entity.ScanEntry) error) error {
		return yield(&entity.ScanEntry{Path: "file", Change: entity.ScanChange_SCAN_CHANGE_CHANGED,
			Mode: 0644, MtimeNs: time.Unix(2, 0).UnixNano(), Size: 5, Sha256: hash[:]})
	})
	require.NoError(t, err)
	stored, err := lib.GetPosition(ctx, position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_DAMAGED, stored.Health)
	require.Equal(t, int64(100), stored.CheckedAt)
}

func TestPositionHealthRejectsMissingDirectoryAndInvalidObservations(t *testing.T) {
	// A health observation belongs to an existing regular Position, never a derived directory.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	directory := &Position{MediaID: 1, Path: "folder/", IsDir: true}
	require.NoError(t, lib.SavePosition(ctx, directory))
	token, err := PositionContentToken(directory)
	require.NoError(t, err)
	observation := &PositionHealthObservation{PositionID: directory.ID, ContentToken: token, Health: entity.PositionHealth_HEALTHY, CheckedAt: 10}
	published, err := lib.PublishPositionHealth(ctx, observation)
	require.NoError(t, err)
	require.False(t, published)
	observation.PositionID++
	published, err = lib.PublishPositionHealth(ctx, observation)
	require.NoError(t, err)
	require.False(t, published)

	// Unknown is the absence of a check and cannot overwrite a completed observation through this API.
	observation.Health = entity.PositionHealth_POSITION_HEALTH_UNKNOWN
	_, err = lib.PublishPositionHealth(ctx, observation)
	require.Error(t, err)
	require.True(t, PositionRestoreEligible(entity.PositionHealth_POSITION_HEALTH_UNKNOWN))
	require.True(t, PositionRestoreEligible(entity.PositionHealth_HEALTHY))
	for _, health := range []entity.PositionHealth{entity.PositionHealth_DAMAGED, entity.PositionHealth_MISSING, entity.PositionHealth_UNREADABLE} {
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
					return yield(&MediaFile{Path: "file", Hash: hash[:], Size: 7, CheckedAt: checkedAt, HealthJobID: 42})
				})
			require.NoError(t, err)

			// Only explicit post-Finalize evidence starts the Position as known healthy.
			positions, err := lib.ListMediaFilePositions(ctx, media.ID, "", 10)
			require.NoError(t, err)
			require.Len(t, positions, 1)
			want := entity.PositionHealth_POSITION_HEALTH_UNKNOWN
			if checkedAt > 0 {
				want = entity.PositionHealth_HEALTHY
				require.Equal(t, int64(42), positions[0].HealthJobID)
			}
			require.Equal(t, want, positions[0].Health)
			require.Equal(t, checkedAt, positions[0].CheckedAt)
		})
	}
}
