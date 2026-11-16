package archive

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// publishUnknownOriginal publishes one original in its own Location without content facts.
func publishUnknownOriginal(t *testing.T, exe *executor.Executor, name string, content []byte) *library.File {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(exe.Paths().Source, name)
	require.NoError(t, os.Mkdir(root, 0o755))
	root, err := exe.LocationRoot(root)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "original.txt"), content, 0o644))
	info, err := os.Stat(filepath.Join(root, "original.txt"))
	require.NoError(t, err)
	source := &library.Location{Name: name, RootPath: root, ExecutorID: "local"}
	require.NoError(t, exe.Lib().CreateLocation(ctx, source))
	_, err = exe.Lib().PublishAnalyzed(ctx, source.ID, 1, func(_ context.Context, yield func(*library.ObservedEntry) error) error {
		return yield(&library.ObservedEntry{Path: "original.txt", Size: info.Size(), Mode: uint32(info.Mode()), MtimeNS: info.ModTime().UnixNano()})
	}, nil)
	require.NoError(t, err)
	rows, err := exe.Lib().LocationOriginalsPage(ctx, source.ID, "", 10)
	require.NoError(t, err)
	require.Empty(t, rows[0].Signature)
	file, err := exe.Lib().GetFile(ctx, rows[0].FileID)
	require.NoError(t, err)
	return file
}

// newTestVolume registers one new Volume and returns its UUID and Media ID.
func newTestVolume(t *testing.T, exe *executor.Executor, name string) (string, int64) {
	t.Helper()
	root := filepath.Join(exe.Paths().Volumes[0], name)
	require.NoError(t, os.Mkdir(root, 0o755))
	volume, err := mediapkg.InitializeVolume(root, &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)
	media, err := exe.Lib().CreateMedia(context.Background(), &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: name,
		Profile: volume.Marker.Profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS,
	})
	require.NoError(t, err)
	return volume.Marker.UUID, media.ID
}

// writeVolume runs one Archive Media attempt on a registered Volume and returns the settled Job.
func writeVolume(t *testing.T, exe *executor.Executor, id int64, uuid string) *executor.Job {
	t.Helper()
	ctx := context.Background()
	_, err := (&service{exe: exe}).WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: id, Target: (&entity.ArchiveVolumeTarget{Uuid: uuid}).Pack(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !exe.IsRunning(id) }, 15*time.Second, 10*time.Millisecond)
	job, err := exe.GetJob(ctx, id)
	require.NoError(t, err)
	return job
}

// createTargetJob creates an Archive of the Files and waits until its preparation settles.
func createTargetJob(t *testing.T, exe *executor.Executor, target *uint32, files ...*library.File) *executor.Job {
	t.Helper()
	ctx := context.Background()
	ids := make([]int64, 0, len(files))
	for _, file := range files {
		ids = append(ids, file.ID)
	}
	created, err := (&service{exe: exe}).Create(ctx, &entity.CreateArchiveJobRequest{
		Spec: &entity.ArchiveJobSpec{Selections: archiveSelections(ids...), TargetMediaCount: target},
	})
	require.NoError(t, err)
	var job *executor.Job
	require.Eventually(t, func() bool {
		job, err = exe.GetJob(ctx, created.Job.Id)
		return err == nil && job.Status != entity.JobStatus_JOB_STATUS_PREPARING && !exe.IsRunning(job.ID)
	}, 5*time.Second, time.Millisecond)
	return job
}

func manifestFileIDs(t *testing.T, exe *executor.Executor, id int64) []int64 {
	t.Helper()
	reply, err := (&service{exe: exe}).ListFiles(context.Background(), &entity.ListArchiveJobFilesRequest{Id: id, Limit: 100})
	require.NoError(t, err)
	ids := make([]int64, 0, len(reply.Items))
	for _, item := range reply.Items {
		ids = append(ids, item.File.Expected.FileId)
	}
	return ids
}

func archiveSkips(t *testing.T, exe *executor.Executor, id int64) (*int64, *int64) {
	t.Helper()
	reply, err := (&service{exe: exe}).GetProgress(context.Background(), &entity.GetArchiveJobProgressRequest{Id: id})
	require.NoError(t, err)
	return reply.SkippedTargetReachedCount, reply.SkippedAlreadyOnMediaCount
}

func contentMedia(t *testing.T, exe *executor.Executor, content []byte) []int64 {
	t.Helper()
	signature, err := library.NewFileSignature(sha256Sum(content), int64(len(content)))
	require.NoError(t, err)
	media, err := exe.Lib().ContentMedia(context.Background(), [][]byte{signature})
	require.NoError(t, err)
	return media[string(signature)]
}

func mediaFileCount(t *testing.T, exe *executor.Executor, mediaID int64) int {
	t.Helper()
	positions, err := exe.Lib().ListMediaFilePositions(context.Background(), mediaID, "", 100)
	require.NoError(t, err)
	return len(positions)
}

// requireVersion checks that the File has a saved version of the content.
func requireVersion(t *testing.T, exe *executor.Executor, file *library.File, content []byte) {
	t.Helper()
	versions, _, err := exe.Lib().ListFileVersions(context.Background(), file.ID, 0, 10)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	hash := sha256.Sum256(content)
	require.Equal(t, hash[:], versions[0].Hash)
}

func TestArchiveTargetMediaCountPreparation(t *testing.T) {
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	api := &service{exe: exe}
	_, archived, _ := publishArchiveOriginal(t, exe, "archived", []byte("archived content"))
	_, known, _ := publishArchiveOriginal(t, exe, "known", []byte("known content"))
	unknown := publishUnknownOriginal(t, exe, "unknown", []byte("unknown content"))
	first := createTargetJob(t, exe, nil, archived)
	archiveToVolume(t, exe, first.ID)
	selections := archiveSelections(archived.ID, known.ID, unknown.ID)

	// Zero is rejected as invalid input by both review and creation.
	_, err := api.Estimate(ctx, &entity.EstimateArchiveJobRequest{Selections: selections, TargetMediaCount: proto.Uint32(0)})
	require.ErrorIs(t, err, executor.ErrInvalidRequest)
	_, err = api.Create(ctx, &entity.CreateArchiveJobRequest{Spec: &entity.ArchiveJobSpec{Selections: selections, TargetMediaCount: proto.Uint32(0)}})
	require.ErrorIs(t, err, executor.ErrInvalidRequest)

	// Review reports the same skip, and only when a target is given.
	estimate, err := api.Estimate(ctx, &entity.EstimateArchiveJobRequest{Selections: selections, TargetMediaCount: proto.Uint32(1)})
	require.NoError(t, err)
	require.EqualValues(t, 3, estimate.Result.FileCount)
	require.Equal(t, proto.Int64(1), estimate.Result.SkippedTargetReachedCount)
	estimate, err = api.Estimate(ctx, &entity.EstimateArchiveJobRequest{Selections: selections})
	require.NoError(t, err)
	require.Nil(t, estimate.Result.SkippedTargetReachedCount)

	// Known content at the target is left out and counted; content below it and unknown content stay.
	job := createTargetJob(t, exe, proto.Uint32(1), archived, known, unknown)
	require.Equal(t, entity.JobStatus_JOB_STATUS_READY, job.Status)
	require.ElementsMatch(t, []int64{known.ID, unknown.ID}, manifestFileIDs(t, exe, job.ID))
	reached, onMedia := archiveSkips(t, exe, job.ID)
	require.Equal(t, proto.Int64(1), reached)
	require.Equal(t, proto.Int64(0), onMedia)
	creation, err := api.GetCreation(ctx, &entity.GetArchiveJobCreationRequest{Id: job.ID})
	require.NoError(t, err)
	require.Equal(t, proto.Uint32(1), creation.Request.Spec.TargetMediaCount)

	// A higher target keeps the content; no target keeps everything and reports no skip counts.
	job = createTargetJob(t, exe, proto.Uint32(2), archived, known, unknown)
	require.ElementsMatch(t, []int64{archived.ID, known.ID, unknown.ID}, manifestFileIDs(t, exe, job.ID))
	reached, _ = archiveSkips(t, exe, job.ID)
	require.Equal(t, proto.Int64(0), reached)
	job = createTargetJob(t, exe, nil, archived, known, unknown)
	require.ElementsMatch(t, []int64{archived.ID, known.ID, unknown.ID}, manifestFileIDs(t, exe, job.ID))
	reached, onMedia = archiveSkips(t, exe, job.ID)
	require.Nil(t, reached)
	require.Nil(t, onMedia)

	// When every selected file is left out, preparation fails with the reason.
	job = createTargetJob(t, exe, proto.Uint32(1), archived)
	require.Equal(t, entity.JobStatus_JOB_STATUS_FAILED, job.Status)
	require.Contains(t, job.Error, "every selected file already reaches the target Media count of 1")
}

func TestArchiveTargetMediaCountSkipsContentAlreadyOnTheChosenMedia(t *testing.T) {
	// One copy exists on the Volume; a second File holds the same content.
	exe := setupTestExecutor(t, executor.Scripts{})
	content := []byte("already archived")
	_, original, _ := publishArchiveOriginal(t, exe, "original", content)
	uuid, mediaID := newTestVolume(t, exe, "first")
	first := createTargetJob(t, exe, nil, original)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, writeVolume(t, exe, first.ID, uuid).Status)
	_, duplicate, _ := publishArchiveOriginal(t, exe, "duplicate", content)

	// Below the target both are prepared; the chosen Media already holds their content, so the
	// APPEND completes without opening the Volume, which is no longer mounted.
	job := createTargetJob(t, exe, proto.Uint32(2), original, duplicate)
	require.Len(t, manifestFileIDs(t, exe, job.ID), 2)
	require.NoError(t, os.Rename(filepath.Join(exe.Paths().Volumes[0], "first"), filepath.Join(t.TempDir(), "unmounted")))
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, writeVolume(t, exe, job.ID, uuid).Status)
	reached, onMedia := archiveSkips(t, exe, job.ID)
	require.Equal(t, proto.Int64(0), reached)
	require.Equal(t, proto.Int64(2), onMedia)
	require.Empty(t, manifestFileIDs(t, exe, job.ID))
	require.Equal(t, 1, mediaFileCount(t, exe, mediaID))

	// Each skipped File's version is linked to the existing copy.
	requireVersion(t, exe, original, content)
	requireVersion(t, exe, duplicate, content)
}

func TestArchiveTargetMediaCountOpensMediaForPartialOverlap(t *testing.T) {
	// The Volume holds one selected content; the other is new.
	exe := setupTestExecutor(t, executor.Scripts{})
	_, archived, _ := publishArchiveOriginal(t, exe, "archived", []byte("already archived"))
	_, fresh, _ := publishArchiveOriginal(t, exe, "fresh", []byte("new content"))
	uuid, mediaID := newTestVolume(t, exe, "volume")
	first := createTargetJob(t, exe, nil, archived)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, writeVolume(t, exe, first.ID, uuid).Status)

	// The overlap settles before opening; the remaining file is still written to the same Volume.
	job := createTargetJob(t, exe, proto.Uint32(2), archived, fresh)
	require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, writeVolume(t, exe, job.ID, uuid).Status)
	reached, onMedia := archiveSkips(t, exe, job.ID)
	require.Equal(t, proto.Int64(0), reached)
	require.Equal(t, proto.Int64(1), onMedia)
	require.Equal(t, []int64{fresh.ID}, manifestFileIDs(t, exe, job.ID))
	require.Equal(t, 2, mediaFileCount(t, exe, mediaID))
	require.Equal(t, []int64{mediaID}, contentMedia(t, exe, []byte("new content")))
}

func TestArchiveTargetMediaCountWritesDuplicateContentOncePerMedia(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target *uint32
		copies int
	}{
		{"target writes once", proto.Uint32(2), 1},
		{"no target writes both", nil, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exe := setupTestExecutor(t, executor.Scripts{})
			content := []byte("duplicated in one Job")
			_, original, _ := publishArchiveOriginal(t, exe, "original", content)
			_, duplicate, _ := publishArchiveOriginal(t, exe, "duplicate", content)
			uuid, mediaID := newTestVolume(t, exe, "volume")
			job := createTargetJob(t, exe, tc.target, original, duplicate)
			require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, writeVolume(t, exe, job.ID, uuid).Status)
			require.Equal(t, tc.copies, mediaFileCount(t, exe, mediaID))
			requireVersion(t, exe, original, content)
			requireVersion(t, exe, duplicate, content)
			if tc.target == nil {
				return
			}
			_, onMedia := archiveSkips(t, exe, job.ID)
			require.Equal(t, proto.Int64(1), onMedia)
		})
	}
}

// firstFileSession accepts only the first target of a real Session, like a Media that fills up.
type firstFileSession struct {
	mediapkg.WriteSession
	targets int
}

func (s *firstFileSession) Target(path string, size int64) (mediapkg.TargetRef, error) {
	s.targets++
	if s.targets > 1 {
		return mediapkg.TargetRef{}, mediapkg.ErrCapacityBoundary
	}
	return s.WriteSession.Target(path, size)
}

func TestArchiveTargetMediaCountAcrossMedia(t *testing.T) {
	for _, tc := range []struct {
		name            string
		target          uint32
		media           int
		reached, copies int64
	}{
		{"second Media raises the count to two", 2, 2, 0, 3},
		{"reached target skips the remaining duplicate", 1, 1, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			exe := setupTestExecutor(t, executor.Scripts{})
			// Target order puts a file that does not fit between the duplicates.
			content := []byte("spans two Media")
			_, original, _ := publishArchiveOriginal(t, exe, "1-original", content)
			_, filler, _ := publishArchiveOriginal(t, exe, "2-filler", []byte("does not fit"))
			_, duplicate, _ := publishArchiveOriginal(t, exe, "3-duplicate", content)
			job := createTargetJob(t, exe, proto.Uint32(tc.target), original, filler, duplicate)
			value, err := exe.GetJobRunner(ctx, job.ID)
			require.NoError(t, err)
			runner := value.(*jobArchiveRunner)

			// The first Media fills after one file; the later duplicate stays pending.
			uuid, _ := newTestVolume(t, exe, "first")
			target := (&entity.ArchiveVolumeTarget{Uuid: uuid}).Pack()
			outcome := make(chan error, 1)
			require.NoError(t, exe.StartJob(ctx, job.ID, entity.JobKind_JOB_KIND_ARCHIVE, func(attempt context.Context, _ executor.Runner) error {
				err := runner.transition(archiveStatePreparingMedia)
				if err == nil {
					var session mediapkg.WriteSession
					if session, err = exe.OpenWriteSession(attempt, job.ID, runner.logger, runner.mediaWait, target); err == nil {
						err = runner.archivePreparedMedia(attempt, target, &firstFileSession{WriteSession: session})
					}
				}
				err = errors.Join(err, runner.transition(archiveStateWaitingForMedia))
				outcome <- err
				return err
			}))
			require.ErrorIs(t, <-outcome, mediapkg.ErrTargetNoSpace)
			require.Eventually(t, func() bool { return !exe.IsRunning(job.ID) }, 5*time.Second, time.Millisecond)
			require.Len(t, manifestFileIDs(t, exe, job.ID), 3)

			// The next Media copies the duplicate only while the content is below the target.
			second, _ := newTestVolume(t, exe, "second")
			require.Equal(t, entity.JobStatus_JOB_STATUS_COMPLETED, writeVolume(t, exe, job.ID, second).Status)
			require.Len(t, contentMedia(t, exe, content), tc.media)
			reached, onMedia := archiveSkips(t, exe, job.ID)
			require.Equal(t, proto.Int64(tc.reached), reached)
			require.Equal(t, proto.Int64(0), onMedia)
			require.Len(t, manifestFileIDs(t, exe, job.ID), int(tc.copies))
			requireVersion(t, exe, duplicate, content)
		})
	}
}
