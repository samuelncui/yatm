package scan

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/acp"
	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/executor"
	"github.com/samuelncui/yatm/library"
	mediapkg "github.com/samuelncui/yatm/media"
	"github.com/samuelncui/yatm/resource"
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
	lib := library.New(libraryDB)
	require.NoError(t, lib.AutoMigrate())
	volumeRoot := filepath.Join(root, "volumes", "disk")
	require.NoError(t, os.MkdirAll(volumeRoot, 0755))
	profile := &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD}
	volume, err := mediapkg.InitializeVolume(volumeRoot, profile)
	require.NoError(t, err)
	media, err := lib.CreateMedia(context.Background(), &library.Media{Kind: entity.MediaKind_MEDIA_KIND_VOLUME,
		Identity: volume.Marker.UUID, Name: "Archive", Profile: profile.Pack(), CreateTime: volume.Marker.CreatedAt})
	require.NoError(t, err)
	exe := executor.New(executorDB, lib, nil, executor.Paths{Work: filepath.Join(root, "work"), Volumes: []string{filepath.Dir(volumeRoot)}}, executor.Scripts{}, preview)
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
		Mode: uint32(stat.Mode()), ModTime: stat.ModTime(), WriteTime: stat.ModTime()}
	require.NoError(t, exe.Lib().SavePosition(context.Background(), position))
	return position
}

func createVerifyJob(t *testing.T, exe *executor.Executor, mediaID int64, volume *mediapkg.Volume) int64 {
	t.Helper()
	// Pause a real automatic Volume attempt at an unavailable source, retaining its frozen baseline.
	away := filepath.Join(t.TempDir(), "offline")
	require.NoError(t, os.Rename(volume.Root, away))
	reply, err := (&service{exe: exe}).Create(context.Background(), &entity.CreateScanJobRequest{Spec: &entity.ScanJobSpec{MediaId: mediaID, SignaturePolicy: entity.ScanSignaturePolicy_FORCE_READ, ResultPolicy: entity.ScanResultPolicy_VERIFY_COPIES}})
	require.NoError(t, err)
	waitVerifyIdle(t, exe, reply.Job.Id, entity.JobStatus_INDEXING)
	require.NoError(t, os.Rename(away, volume.Root))
	return reply.Job.Id
}

func waitVerifyIdle(t *testing.T, exe *executor.Executor, id int64, status entity.JobStatus) {
	t.Helper()
	require.Eventually(t, func() bool { return !exe.IsRunning(id) }, 10*time.Second, 5*time.Millisecond)
	job, err := exe.GetJob(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, status, job.Status)
}

func runVerifyVolume(t *testing.T, exe *executor.Executor, id int64, _ *mediapkg.Volume) {
	t.Helper()
	require.NoError(t, exe.RetryIndex(context.Background(), id))
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
	id := createVerifyJob(t, exe, media.ID, volume)
	runVerifyVolume(t, exe, id, volume)
	waitVerifyIdle(t, exe, id, entity.JobStatus_COMPLETED)
	progress, err := (&service{exe: exe}).GetProgress(ctx, &entity.GetScanJobProgressRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, int64(2), progress.Matched)
	require.Equal(t, int64(1), progress.Damaged)
	require.Equal(t, int64(1), progress.Missing)
	require.Equal(t, int64(1), progress.Unreadable)
	require.Equal(t, int64(1), progress.Unverifiable)
	require.Zero(t, progress.Progress.TotalFiles-progress.Matched-progress.Damaged-progress.Missing-progress.Unreadable-progress.Unverifiable)
	stored, err := exe.Lib().GetPosition(ctx, damaged.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_DAMAGED, stored.Health)
	require.Equal(t, damaged.Hash, stored.Hash)
	require.Equal(t, damaged.Signature, stored.Signature)
	stored, err = exe.Lib().GetPosition(ctx, unknown.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
	require.Zero(t, stored.CheckedAt)

	// Result pagination remains stable across several requests and exposes every frozen item exactly once.
	var entries []*entity.ScanEntry
	var after int64
	for {
		reply, err := (&service{exe: exe}).ListEntries(ctx, &entity.ListScanJobEntriesRequest{Id: id, Limit: 2, AfterId: &after})
		require.NoError(t, err)
		entries = append(entries, reply.Entries...)
		if !reply.HasMore {
			break
		}
		after = reply.Entries[len(reply.Entries)-1].Id
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
		require.Equal(t, expected.Size, entry.Size)
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
	cache := &singleRead{filename: filename}
	require.NoError(t, acp.RunStream(context.Background(), cache, cache, acp.WithHash(true), acp.WithSignatureCache(true)))
	_, valid, err := acp.ReadCachedSignature(filename)
	require.NoError(t, err)
	if !valid {
		t.Skip("temporary filesystem does not support signature xattrs")
	}

	// Keep cache-key metadata unchanged while replacing the real bytes.
	require.NoError(t, os.WriteFile(filename, []byte("change"), 0644))
	require.NoError(t, os.Chtimes(filename, position.ModTime, position.ModTime))
	_, valid, err = acp.ReadCachedSignature(filename)
	require.NoError(t, err)
	require.True(t, valid)
	id := createVerifyJob(t, exe, media.ID, volume)
	runVerifyVolume(t, exe, id, volume)
	waitVerifyIdle(t, exe, id, entity.JobStatus_COMPLETED)
	stored, err := exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_DAMAGED, stored.Health)

	// Verify never refreshes that cache; inspection is a read-only physical operation.
	signature, valid, err := acp.ReadCachedSignature(filename)
	require.NoError(t, err)
	require.True(t, valid)
	require.Equal(t, position.Hash, signature.SHA256[:])
}

type singleRead struct {
	filename string
	used     bool
}

func (s *singleRead) Next(context.Context) (*acp.StreamRequest, error) {
	if s.used {
		return nil, io.EOF
	}
	s.used = true
	return &acp.StreamRequest{ID: 1, Source: s.filename}, nil
}
func (*singleRead) Write(context.Context, *acp.StreamResult) error { return nil }
func (*singleRead) Flush(context.Context) error                    { return nil }

func TestVerifyUnavailableMediaLeavesInventoryAndFindingsPending(t *testing.T) {
	// Index while available, then make the mounted Media inaccessible before the read attempt.
	exe, volume, media := setupVerifyExecutor(t)
	position := saveVerifyFile(t, exe, volume, media, "file", []byte("content"))
	id := createVerifyJob(t, exe, media.ID, volume)
	away := filepath.Join(t.TempDir(), "away")
	require.NoError(t, os.Rename(volume.Root, away))
	runVerifyVolume(t, exe, id, volume)
	waitVerifyIdle(t, exe, id, entity.JobStatus_INDEXING)
	stored, err := exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
	require.Zero(t, stored.CheckedAt)

	// Returning the same mounted identity permits a complete retry of the frozen expectations.
	require.NoError(t, os.Rename(away, volume.Root))
	runVerifyVolume(t, exe, id, volume)
	waitVerifyIdle(t, exe, id, entity.JobStatus_COMPLETED)
	stored, err = exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_HEALTHY, stored.Health)
}

type checkSession struct {
	root     string
	media    *library.Media
	finalize func(context.Context) error
}

func (*checkSession) Capabilities() mediapkg.Capabilities {
	return mediapkg.Capabilities{Read: mediapkg.AccessRandom}
}
func (s *checkSession) Inspect() *library.Media { return s.media }
func (s *checkSession) SourcePath(name string) (string, error) {
	return mediapkg.ResolveSourcePath(s.root, name)
}
func (s *checkSession) Finalize(ctx context.Context) error { return s.finalize(ctx) }

func TestVerifyFinalizeFailurePublishesNoHealth(t *testing.T) {
	// Freeze a good file, then reject the final physical identity after it has been read.
	exe, volume, media := setupVerifyExecutor(t)
	position := saveVerifyFile(t, exe, volume, media, "file", []byte("content"))
	id := createVerifyJob(t, exe, media.ID, volume)
	value, err := exe.GetJobRunner(context.Background(), id)
	require.NoError(t, err)
	r := value.(*runner)
	var config Config
	err = r.db.First(&config, 1).Error
	require.NoError(t, err)
	finalized := 0
	err = r.runPipeline(context.Background(), &config, &Scope{}, nil, &checkSession{root: volume.Root, media: media, finalize: func(context.Context) error {
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
	require.Equal(t, entity.ScanFinding_MATCH, entry.Finding)
	require.False(t, entry.Published)
}

func TestVerifyPublicationRejectsStaleInventory(t *testing.T) {
	// The expected checksum remains frozen even when inventory changes before final publication.
	exe, volume, media := setupVerifyExecutor(t)
	position := saveVerifyFile(t, exe, volume, media, "file", []byte("content"))
	id := createVerifyJob(t, exe, media.ID, volume)
	value, err := exe.GetJobRunner(context.Background(), id)
	require.NoError(t, err)
	r := value.(*runner)
	var config Config
	err = r.db.First(&config, 1).Error
	require.NoError(t, err)
	err = r.runPipeline(context.Background(), &config, &Scope{}, nil, &checkSession{root: volume.Root, media: media, finalize: func(ctx context.Context) error {
		position.Hash = make([]byte, sha256.Size)
		return exe.Lib().SavePosition(ctx, position)
	}})
	require.NoError(t, err)

	// The result remains visible as stale and never updates the new inventory's health or checksum.
	stored, err := exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_POSITION_HEALTH_UNKNOWN, stored.Health)
	require.Equal(t, make([]byte, sha256.Size), stored.Hash)
	var entry Entry
	require.NoError(t, r.db.First(&entry, position.ID).Error)
	require.True(t, entry.Stale)
	require.True(t, entry.Published)
}

func TestVerifyIndexesMultiplePages(t *testing.T) {
	// A manifest larger than one batch stays paged without requiring any physical scan.
	exe, volume, media := setupVerifyExecutor(t)
	for index := 0; index < batchSize+5; index++ {
		position := &library.Position{MediaID: media.ID, Path: fmt.Sprintf("file-%04d", index), Hash: make([]byte, sha256.Size), Size: 0}
		require.NoError(t, exe.Lib().SavePosition(context.Background(), position))
	}
	id := createVerifyJob(t, exe, media.ID, volume)
	progress, err := (&service{exe: exe}).GetProgress(context.Background(), &entity.GetScanJobProgressRequest{Id: id})
	require.NoError(t, err)
	require.Equal(t, int64(batchSize+5), progress.Progress.TotalFiles)
}

func TestVerifyPublicationCheckpointCanRetry(t *testing.T) {
	// Inject a failure after the authoritative health commit but before its Job checkpoint.
	exe, volume, media := setupVerifyExecutor(t)
	position := saveVerifyFile(t, exe, volume, media, "file", []byte("content"))
	id := createVerifyJob(t, exe, media.ID, volume)
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
	runVerifyVolume(t, exe, id, volume)
	waitVerifyIdle(t, exe, id, entity.JobStatus_INDEXING)
	stored, err := exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, entity.PositionHealth_HEALTHY, stored.Health)

	// Retry never changes the expected bytes and can revalidate/checkpoint the same Position.
	require.NoError(t, r.db.Callback().Update().Remove("verify-fail-checkpoint"))
	runVerifyVolume(t, exe, id, volume)
	waitVerifyIdle(t, exe, id, entity.JobStatus_COMPLETED)
	stored, err = exe.Lib().GetPosition(context.Background(), position.ID)
	require.NoError(t, err)
	require.Equal(t, position.Hash, stored.Hash)
}
