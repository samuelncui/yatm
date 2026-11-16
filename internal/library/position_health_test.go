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
	require.NoError(t, lib.db.Create(position).Error)
	observation := &PositionHealthObservation{PositionID: position.ID, Health: entity.PositionHealth_POSITION_HEALTH_HEALTHY, CheckedAtNS: 100, JobID: 2}

	// The completed result changes health without changing its content baseline.
	require.NoError(t, lib.PublishPositionHealth(ctx, observation))
	observation.Health, observation.CheckedAtNS = entity.PositionHealth_POSITION_HEALTH_DAMAGED, 200
	require.NoError(t, lib.PublishPositionHealth(ctx, observation))
	stored, err := readTestPosition(lib, position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_DAMAGED, stored.Health)
	require.Equal(t, hash[:], stored.Hash)
	require.Equal(t, []byte("opaque"), stored.Signature)
}

func TestPositionHealthConcurrentIndependentPublications(t *testing.T) {
	// A concurrent-capable GORM handle lets independent checks reach write callbacks together.
	ctx := context.Background()
	db, lib := newTestLibrary(t)
	const checks = 8
	positions := make([]*Position, checks)
	for i := range positions {
		positions[i] = &Position{MediaID: 1, Path: fmt.Sprintf("file-%d", i)}
		require.NoError(t, lib.db.Create(positions[i]).Error)
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
		stored, err := readTestPosition(lib, position.ID)
		require.NoError(t, err)
		require.Equal(t, entity.PositionHealth_POSITION_HEALTH_HEALTHY, stored.Health)
	}
}

func TestScanPreservesHealthOnlyForUnchangedContent(t *testing.T) {
	// Publish one copy through Media commit and record a completed damaged check.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	hash := sha256.Sum256([]byte("saved"))
	media, err := lib.CommitMedia(ctx, &Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: "11111111-1111-1111-1111-111111111111", Profile: (&entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}).Pack()},
		func(_ context.Context, yield func(*MediaFile) error) error {
			return yield(&MediaFile{Path: "file", Hash: hash[:], Size: 5, Mode: 0644, ModTime: time.Unix(1, 0), WriteTime: time.Unix(1, 0)})
		})
	require.NoError(t, err)
	positions, err := lib.ListMediaFilePositions(ctx, media.ID, "", 10)
	require.NoError(t, err)
	require.Len(t, positions, 1)
	position := positions[0]
	require.NoError(t, lib.PublishPositionHealth(ctx, &PositionHealthObservation{PositionID: position.ID,
		Health: entity.PositionHealth_POSITION_HEALTH_DAMAGED, CheckedAtNS: 100, JobID: 2}))

	// An identical hash/size with changed mtime retains the bad check; Scan is not integrity verification.
	_, err = lib.ApplyScan(ctx, media.ID, func(_ context.Context, yield func(*entity.ScanEntry) error) error {
		return yield(&entity.ScanEntry{Path: "file", Change: entity.ScanChange_SCAN_CHANGE_CHANGED,
			Mode: 0644, MtimeNs: time.Unix(2, 0).UnixNano(), SizeBytes: 5, Sha256: hash[:]})
	})
	require.NoError(t, err)
	stored, err := readTestPosition(lib, position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_DAMAGED, stored.Health)
	require.Equal(t, int64(100), stored.CheckedAtNS)

	// Changed expected content clears the old health rather than transferring it to another content identity.
	changed := sha256.Sum256([]byte("other"))
	_, err = lib.ApplyScan(ctx, media.ID, func(_ context.Context, yield func(*entity.ScanEntry) error) error {
		return yield(&entity.ScanEntry{Path: "file", Change: entity.ScanChange_SCAN_CHANGE_CHANGED,
			Mode: 0644, MtimeNs: time.Unix(3, 0).UnixNano(), SizeBytes: 5, Sha256: changed[:]})
	})
	require.NoError(t, err)
	stored, err = readTestPosition(lib, position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
	require.Zero(t, stored.CheckedAtNS)
	require.Zero(t, stored.HealthJobID)
}

func TestPositionHealthRejectsMissingDirectoryAndInvalidObservations(t *testing.T) {
	// A health observation belongs to an existing regular Position, never a derived directory.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	directory := &Position{MediaID: 1, Path: "folder/", IsDir: true}
	require.NoError(t, lib.db.Create(directory).Error)
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

func TestContentMediaCountsDistinctRestoreCandidateMedia(t *testing.T) {
	// One content has two copies on Media 1, one on Media 2, and excluded copies elsewhere.
	ctx := context.Background()
	_, lib := newTestLibrary(t)
	for id := int64(1); id <= 8; id++ {
		media := &Media{ID: id, Kind: entity.MediaKind_MEDIA_KIND_TAPE, Identity: fmt.Sprintf("TAPE%02d", id), Profile: (&entity.TapeMediaProfile{}).Pack()}
		if id == 8 {
			media.DestroyedAtNS = new(int64)
		}
		require.NoError(t, lib.db.Create(media).Error)
	}
	content, other := []byte("content"), []byte("other")
	positions := []*Position{
		{MediaID: 8, Path: "a", Signature: content},
		{MediaID: 9, Path: "a", Signature: content},
		{MediaID: 1, Path: "a", Signature: content},
		{MediaID: 1, Path: "b", Signature: content, Health: entity.PositionHealth_POSITION_HEALTH_HEALTHY},
		{MediaID: 2, Path: "a", Signature: content},
		{MediaID: 3, Path: "a", Signature: content, Health: entity.PositionHealth_POSITION_HEALTH_DAMAGED},
		{MediaID: 4, Path: "a", Signature: content, Health: entity.PositionHealth_POSITION_HEALTH_MISSING},
		{MediaID: 5, Path: "a", Signature: content, Health: entity.PositionHealth_POSITION_HEALTH_UNREADABLE},
		{MediaID: 6, Path: "dir/", Signature: content, IsDir: true},
		{MediaID: 7, Path: "a", Signature: other},
	}
	for _, position := range positions {
		require.NoError(t, lib.db.Create(position).Error)
	}

	// Same-Media copies count once; damaged, missing, unreadable, directory, destroyed-Media and
	// unregistered-Media rows never count.
	media, err := lib.ContentMedia(ctx, [][]byte{content, other, []byte("absent")})
	require.NoError(t, err)
	require.Equal(t, map[string][]int64{string(content): {1, 2}, string(other): {7}}, media)
	empty, err := lib.ContentMedia(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
