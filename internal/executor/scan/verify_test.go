package scan

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/samuelncui/yatm/internal/resource"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupVerifyExecutor(t *testing.T) (*executor.Executor, *mediapkg.Volume, *library.Media) {
	t.Helper()
	return setupVerifyExecutorWithPreview(t, nil)
}

func setupVerifyExecutorWithPreview(t *testing.T, preview executor.Previewer) (*executor.Executor, *mediapkg.Volume, *library.Media) {
	t.Helper()
	// Use only isolated databases and an ordinary initialized mounted-directory fixture.
	root := t.TempDir()
	executorDB, err := resource.OpenSQLite(filepath.Join(root, "executor.db"))
	require.NoError(t, err)
	libraryDB, err := resource.OpenSQLite(filepath.Join(root, "library.db"))
	require.NoError(t, err)
	lib := library.NewWithSettings(libraryDB, testSettings(t, libraryDB))
	require.NoError(t, lib.AutoMigrate())
	volumeRoot := filepath.Join(root, "volumes", "disk")
	require.NoError(t, os.MkdirAll(volumeRoot, 0755))
	profile := &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}
	volume, err := mediapkg.InitializeVolume(volumeRoot, profile)
	require.NoError(t, err)
	media, err := lib.CreateMedia(context.Background(), &library.Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: volume.Marker.UUID, Name: "Archive", Profile: profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS})
	require.NoError(t, err)
	exe := executor.New(
		executorDB, lib, nil,
		executor.Paths{Work: filepath.Join(root, "work"), Volumes: []string{filepath.Dir(volumeRoot)}},
		executor.Scripts{}, preview,
	)
	require.NoError(t, exe.AutoMigrate())
	return exe, volume, media
}

func saveVerifyFile(t *testing.T, exe *executor.Executor, volume *mediapkg.Volume, media *library.Media, name string, data []byte) *library.Position {
	t.Helper()
	// Make the catalog checksum an independent baseline for the physical fixture.
	filename := filepath.Join(volume.Root, name)
	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0755))
	require.NoError(t, os.WriteFile(filename, data, 0644))
	stat, err := os.Stat(filename)
	require.NoError(t, err)
	hash := sha256.Sum256(data)
	position := &library.Position{MediaID: media.ID, Path: name, Size: int64(len(data)), Hash: hash[:], Signature: []byte("opaque-" + name),
		Mode: uint32(stat.Mode()), MtimeNS: stat.ModTime().UnixNano(), WrittenAtNS: stat.ModTime().UnixNano()}
	require.NoError(t, exe.Lib().SavePosition(context.Background(), position))
	return position
}

func createVerifyJob(t *testing.T, exe *executor.Executor, mediaID int64, volume *mediapkg.Volume) (int64, func()) {
	t.Helper()
	// Freeze the baseline in one automatic attempt and pause before physical access for fault injection.
	ctx := context.Background()
	release, err := exe.AcquireJobResource(ctx, "volume:"+volume.Marker.UUID, nil)
	require.NoError(t, err)
	var released sync.Once
	reply, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{MediaId: mediaID, SignaturePolicy: entity.ScanSignaturePolicy_SCAN_SIGNATURE_POLICY_FORCE_READ, ResultPolicy: entity.ScanResultPolicy_SCAN_RESULT_POLICY_VERIFY_COPIES}})
	require.NoError(t, err)
	t.Cleanup(func() {
		if exe.IsRunning(reply.Job.Id) {
			require.NoError(t, exe.Cancel(reply.Job.Id))
		}
		released.Do(release)
		require.Eventually(t, func() bool { return !exe.IsRunning(reply.Job.Id) }, 10*time.Second, time.Millisecond)
	})
	require.Eventually(t, func() bool {
		job, err := exe.GetJob(ctx, reply.Job.Id)
		return err == nil && job.Phase == entity.JobPhase_JOB_PHASE_QUEUED
	}, 10*time.Second, time.Millisecond)
	return reply.Job.Id, func() {
		released.Do(release)
		require.Eventually(t, func() bool { return !exe.IsRunning(reply.Job.Id) }, 15*time.Second, time.Millisecond)
	}
}

func waitVerifyIdle(t *testing.T, exe *executor.Executor, id int64, status entity.JobStatus) {
	t.Helper()
	require.Eventually(t, func() bool { return !exe.IsRunning(id) }, 10*time.Second, 5*time.Millisecond)
	job, err := exe.GetJob(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, status, job.Status)
}

func TestVerifyVolumeFindingsNeverRewriteExpectedContent(t *testing.T) {
	// Publish ordinary, zero-byte, subsequently damaged, missing, and unverifiable archive entries.
	ctx := context.Background()
	exe, volume, media := setupVerifyExecutor(t)
	good := saveVerifyFile(t, exe, volume, media, "good", []byte("good"))
	saveVerifyFile(t, exe, volume, media, "empty", nil)
	damaged := saveVerifyFile(t, exe, volume, media, "damaged", []byte("before"))
	missing := saveVerifyFile(t, exe, volume, media, "missing", []byte("gone"))
	unknown := saveVerifyFile(t, exe, volume, media, "unknown", []byte("unverified"))
	unknown.Hash = nil
	require.NoError(t, exe.Lib().SavePosition(ctx, unknown))
	unreadable := saveVerifyFile(t, exe, volume, media, "unsafe", []byte("safe"))
	require.NoError(t, os.WriteFile(filepath.Join(volume.Root, damaged.Path), []byte("damage"), 0644))
	require.NoError(t, os.Remove(filepath.Join(volume.Root, missing.Path)))
	require.NoError(t, os.Remove(filepath.Join(volume.Root, unreadable.Path)))
	require.NoError(t, os.Symlink(filepath.Join(volume.Root, good.Path), filepath.Join(volume.Root, unreadable.Path)))

	// Full verification completes with findings instead of adopting the observed damaged hash as correct.
	id, run := createVerifyJob(t, exe, media.ID, volume)
	run()
	waitVerifyIdle(t, exe, id, entity.JobStatus_JOB_STATUS_COMPLETED)
	progress, err := (&service{exe: exe}).GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, int64(2), progress.MatchedCount)
	require.Equal(t, int64(1), progress.DamagedCount)
	require.Equal(t, int64(1), progress.MissingCount)
	require.Equal(t, int64(1), progress.UnreadableCount)
	require.Equal(t, int64(1), progress.UnverifiableCount)
	require.Zero(t, progress.Progress.TotalFileCount-progress.MatchedCount-progress.DamagedCount-progress.MissingCount-progress.UnreadableCount-progress.UnverifiableCount)
	stored, err := exe.Lib().GetPosition(ctx, damaged.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_DAMAGED, stored.Health)
	require.Equal(t, damaged.Hash, stored.Hash)
	require.Equal(t, damaged.Signature, stored.Signature)
	stored, err = exe.Lib().GetPosition(ctx, unknown.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
	require.Zero(t, stored.CheckedAtNS)

	// Result pagination remains stable across several requests and exposes every frozen item exactly once.
	var entries []*entity.ScanEntry
	var after string
	for {
		reply, err := (&service{exe: exe}).ListEntries(ctx, &entity.ListScanJobEntriesRequest{Id: id, Limit: 2, Cursor: after, IncludeTotal: true})
		require.NoError(t, err)
		// The total describes the complete manifest, not the remaining page window.
		require.Equal(t, int64(6), reply.GetTotalEntryCount())
		entries = append(entries, reply.Entries...)
		if !reply.HasMore {
			break
		}
		after = strconv.FormatInt(reply.Entries[len(reply.Entries)-1].Id, 10)
	}
	require.Len(t, entries, 6)
	byPath := make(map[string]*entity.ScanEntry, len(entries))
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	for _, expected := range []*library.Position{good, damaged, missing, unreadable, unknown} {
		entry := byPath[expected.Path]
		require.Equal(t, expected.Hash, entry.Sha256)
		require.Equal(t, expected.Signature, entry.Signature)
		require.Equal(t, expected.Size, entry.SizeBytes)
	}
	actual := sha256.Sum256([]byte("damage"))
	require.Equal(t, actual[:], byPath[damaged.Path].ActualHash)
	require.NotEqual(t, byPath[damaged.Path].Sha256, byPath[damaged.Path].ActualHash)
	require.Empty(t, byPath[missing.Path].ActualHash)
}

func TestVerifyIgnoresValidSignatureCache(t *testing.T) {
	// Prime the disposable ACP cache using the original bytes and metadata.
	exe, volume, media := setupVerifyExecutor(t)
	position := saveVerifyFile(t, exe, volume, media, "cached", []byte("before"))
	filename := filepath.Join(volume.Root, position.Path)
	primeSignatureCache(t, filename)
	_, valid, err := acp.ReadCachedSignature(filename)
	require.NoError(t, err)
	if !valid {
		t.Skip("temporary filesystem does not support signature xattrs")
	}

	// Keep cache-key metadata unchanged while replacing the real bytes.
	require.NoError(t, os.WriteFile(filename, []byte("change"), 0644))
	require.NoError(t, os.Chtimes(filename, time.Unix(0, position.MtimeNS), time.Unix(0, position.MtimeNS)))
	_, valid, err = acp.ReadCachedSignature(filename)
	require.NoError(t, err)
	require.True(t, valid)
	id, run := createVerifyJob(t, exe, media.ID, volume)
	run()
	waitVerifyIdle(t, exe, id, entity.JobStatus_JOB_STATUS_COMPLETED)
	stored, err := exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_DAMAGED, stored.Health)

	// Verify never refreshes that cache; inspection is a read-only physical operation.
	signature, valid, err := acp.ReadCachedSignature(filename)
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, position.Hash, signature.SHA256[:])
}

// cachePrimingItem is one targetless file whose outcome the results callback records.
type cachePrimingItem struct {
	filename string
	source   *cachePrimingSource
}

func (i *cachePrimingItem) Source() string    { return i.filename }
func (i *cachePrimingItem) Targets() []string { return nil }

type cachePrimingSource struct {
	filename string
	failure  error
}

// primeSignatureCache hashes one file through ACP so a later run can reuse the stored value.
func primeSignatureCache(t *testing.T, filename string) {
	t.Helper()
	source := &cachePrimingSource{filename: filename}
	item := &cachePrimingItem{filename: filename, source: source}
	engine, err := acp.NewStream(context.Background(), func(results []acp.Result) error {
		for _, result := range results {
			if result.Err != nil {
				source.failure = result.Err
			}
		}
		return nil
	}, acp.WithHashPolicy(acp.HashReadRefresh))
	require.NoError(t, err)
	require.NoError(t, engine.Submit(item))
	require.NoError(t, engine.Close())
	require.NoError(t, engine.Wait())
	require.NoError(t, source.failure)
}

func TestVerifyUnavailableMediaLeavesInventoryAndFindingsPending(t *testing.T) {
	// Freeze the expectation, then make the mounted Media inaccessible before the queued read.
	exe, volume, media := setupVerifyExecutor(t)
	position := saveVerifyFile(t, exe, volume, media, "file", []byte("content"))
	id, run := createVerifyJob(t, exe, media.ID, volume)
	away := filepath.Join(t.TempDir(), "away")
	require.NoError(t, os.Rename(volume.Root, away))
	run()
	waitVerifyIdle(t, exe, id, entity.JobStatus_JOB_STATUS_FAILED)
	stored, err := exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
	require.Zero(t, stored.CheckedAtNS)

	// A failed Scan cannot run again, even when the same mounted identity becomes available.
	require.NoError(t, os.Rename(away, volume.Root))
	_, err = (&service{exe: exe}).ReadMedia(context.Background(), &entity.ReadScanMediaRequest{
		Id: id, Target: (&entity.ReadVolumeTarget{Uuid: volume.Marker.UUID}).Pack(),
	})
	require.ErrorContains(t, err, "cannot start from status JOB_STATUS_FAILED")
	fresh, run := createVerifyJob(t, exe, media.ID, volume)
	run()
	waitVerifyIdle(t, exe, fresh, entity.JobStatus_JOB_STATUS_COMPLETED)
	stored, err = exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_HEALTHY, stored.Health)
}

type checkSession struct {
	root     string
	media    *library.Media
	finalize func(context.Context) error
}

func (*checkSession) Capabilities() mediapkg.Capabilities {
	return mediapkg.Capabilities{Read: mediapkg.AccessRandom}
}
func (s *checkSession) Media() *mediapkg.Descriptor {
	if s.media == nil {
		return nil
	}
	var destroyed *time.Time
	if s.media.DestroyedAtNS != nil {
		stamp := time.Unix(0, *s.media.DestroyedAtNS)
		destroyed = &stamp
	}
	return &mediapkg.Descriptor{
		ID:            s.media.ID,
		Kind:          s.media.Kind,
		Identity:      s.media.Identity,
		Name:          s.media.Name,
		Profile:       s.media.Profile,
		CreateTime:    time.Unix(0, s.media.CreatedAtNS),
		DestroyTime:   destroyed,
		CapacityBytes: s.media.CapacityBytes,
		WrittenBytes:  s.media.WrittenBytes,
	}
}
func (s *checkSession) SourcePath(name string) (string, error) {
	return mediapkg.ResolveSourcePath(s.root, name)
}
func (s *checkSession) Finalize(ctx context.Context) error { return s.finalize(ctx) }

func TestVerifyFinalizeFailurePublishesNoHealth(t *testing.T) {
	// Freeze a good file, then reject the final physical identity after it has been read.
	exe, volume, media := setupVerifyExecutor(t)
	position := saveVerifyFile(t, exe, volume, media, "file", []byte("content"))
	id, _ := createVerifyJob(t, exe, media.ID, volume)
	value, err := exe.GetJobRunner(context.Background(), id)
	require.NoError(t, err)
	r := value.(*runner)
	var config Config
	err = r.db.First(&config, 1).Error
	require.NoError(t, err)
	finalized := 0
	err = r.runPipeline(testAttemptContext(context.Background()), &config, &Scope{}, nil, &checkSession{root: volume.Root, media: media, finalize: func(context.Context) error {
		finalized++
		return errors.New("identity changed")
	}})
	require.ErrorContains(t, err, "identity changed")
	require.Equal(t, 1, finalized)

	// Tentative Job observations are useful diagnostics but must not certify the current Position.
	stored, err := exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
	var entry Entry
	require.NoError(t, r.db.First(&entry, position.ID).Error)
	require.Equal(t, entity.ScanFinding_SCAN_FINDING_MATCH, entry.Finding)
	require.False(t, entry.Published)
}

func TestVerifyIndexesMultiplePages(t *testing.T) {
	// A manifest larger than one batch stays paged without requiring any physical scan.
	exe, volume, media := setupVerifyExecutor(t)
	for index := 0; index < batchSize+5; index++ {
		position := &library.Position{MediaID: media.ID, Path: fmt.Sprintf("file-%04d", index), Hash: make([]byte, sha256.Size), Size: 0}
		require.NoError(t, exe.Lib().SavePosition(context.Background(), position))
	}
	id, _ := createVerifyJob(t, exe, media.ID, volume)
	page, err := (&service{exe: exe}).ListEntries(context.Background(), &entity.ListScanJobEntriesRequest{Id: id, Limit: 2, IncludeTotal: true})
	require.NoError(t, err)
	require.Equal(t, int64(batchSize+5), page.GetTotalEntryCount())
	require.Len(t, page.Entries, 2)
	require.True(t, page.HasMore)
}

func TestVerifyPublicationCheckpointFailureRetainsHealthAndEndsJob(t *testing.T) {
	// Inject a failure after the authoritative health commit but before its Job checkpoint.
	exe, volume, media := setupVerifyExecutor(t)
	position := saveVerifyFile(t, exe, volume, media, "file", []byte("content"))
	id, run := createVerifyJob(t, exe, media.ID, volume)
	value, err := exe.GetJobRunner(context.Background(), id)
	require.NoError(t, err)
	r := value.(*runner)
	require.NoError(t, r.db.Callback().Update().Before("gorm:update").Register("verify-fail-checkpoint", func(tx *gorm.DB) {
		if tx.Statement.Table != "entries" {
			return
		}
		row, ok := tx.Statement.Dest.(*Entry)
		if ok && row.Published {
			tx.AddError(errors.New("checkpoint failed"))
		}
	}))
	run()
	waitVerifyIdle(t, exe, id, entity.JobStatus_JOB_STATUS_FAILED)
	stored, err := exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_HEALTHY, stored.Health)

	// A new Scan observes the same authoritative Position; the failed Job stays terminal.
	require.NoError(t, r.db.Callback().Update().Remove("verify-fail-checkpoint"))
	_, err = (&service{exe: exe}).ReadMedia(context.Background(), &entity.ReadScanMediaRequest{
		Id: id, Target: (&entity.ReadVolumeTarget{Uuid: volume.Marker.UUID}).Pack(),
	})
	require.ErrorContains(t, err, "cannot start from status JOB_STATUS_FAILED")
	fresh, run := createVerifyJob(t, exe, media.ID, volume)
	run()
	waitVerifyIdle(t, exe, fresh, entity.JobStatus_JOB_STATUS_COMPLETED)
	stored, err = exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, position.Hash, stored.Hash)
}
