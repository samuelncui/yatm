package archive

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/samuelncui/yatm/entity"
	"github.com/samuelncui/yatm/internal/executor"
	"github.com/samuelncui/yatm/internal/library"
	mediapkg "github.com/samuelncui/yatm/internal/media"
	"github.com/stretchr/testify/require"
)

func TestArchiveTransfersLiteralUTF8Filenames(t *testing.T) {
	// Distinct native files include a literal backslash and a slash-separated sibling.
	ctx := context.Background()
	exe := setupTestExecutor(t, executor.Scripts{})
	names := []string{
		`back\slash.txt`, "back/slash.txt", " leading.txt", "trailing ", " \t\n",
		"quote'\"\n照片.txt", " parent\\'\"\n / \t/ leaf ",
	}
	contents := make(map[string][]byte, len(names))
	sources := make([]*archiveTestSource, 0, len(names))
	for _, name := range names {
		filename := filepath.Join(exe.Paths().Source, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0o755))
		contents[name] = []byte("content for " + name)
		require.NoError(t, os.WriteFile(filename, contents[name], 0o644))
		sources = append(sources, &archiveTestSource{Base: exe.Paths().Source, Path: []string{name}})
	}
	job := createArchiveJob(t, exe, sources...)
	waitIndexed(t, exe, job.ID)
	value, err := exe.GetJobRunner(ctx, job.ID)
	require.NoError(t, err)
	runner := value.(*jobArchiveRunner)

	// Run the real Archive transfer and publication against a Volume with a literal root name.
	volumeRoot := filepath.Join(exe.Paths().Volumes[0], " volume\\'\"\n ")
	require.NoError(t, os.Mkdir(volumeRoot, 0o755))
	volume, err := mediapkg.InitializeVolume(volumeRoot, &entity.VolumeMediaProfile{Type: entity.VolumeType_VOLUME_TYPE_HDD})
	require.NoError(t, err)
	stored, err := exe.Lib().CreateMedia(ctx, &library.Media{
		Kind: entity.MediaKind_MEDIA_KIND_VOLUME, Identity: volume.Marker.UUID, Name: "Archive",
		Profile: volume.Marker.Profile.Pack(), CreatedAtNS: volume.Marker.CreatedAtNS,
	})
	require.NoError(t, err)
	_, err = (&service{exe: exe}).WriteMedia(ctx, &entity.WriteArchiveMediaRequest{
		Id: job.ID, Target: (&entity.ArchiveVolumeTarget{Uuid: volume.Marker.UUID}).Pack(),
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		record := new(executor.JobRecord)
		err := runner.db.First(record, 1).Error
		return err == nil && record.Status == entity.JobStatus_JOB_STATUS_COMPLETED && !exe.IsRunning(job.ID)
	}, 15*time.Second, 10*time.Millisecond)

	// Every frozen target maps to its own transferred bytes, Position and saved File content.
	var items []*Item
	require.NoError(t, runner.db.Order("id").Find(&items).Error)
	require.Len(t, items, len(names))
	positions, err := exe.Lib().ListMediaFilePositions(ctx, stored.ID, "", 100)
	require.NoError(t, err)
	require.Len(t, positions, len(names))
	byPath := make(map[string]*library.Position, len(positions))
	for _, position := range positions {
		byPath[position.Path] = position
	}
	seen := make(map[string]bool, len(names))
	for _, item := range items {
		want, exists := contents[item.TargetPath]
		require.True(t, exists, "unexpected target %q", item.TargetPath)
		require.False(t, seen[item.TargetPath])
		seen[item.TargetPath] = true
		require.Equal(t, entity.CopyStatus_COPY_STATUS_SUBMITTED, item.Status)
		require.Equal(t, filepath.Join(exe.Paths().Source, filepath.FromSlash(item.TargetPath)), item.Data.SourcePath)
		actual, err := os.ReadFile(filepath.Join(volume.Root, filepath.FromSlash(item.MediaPath)))
		require.NoError(t, err)
		require.Equal(t, want, actual)
		position := byPath[item.MediaPath]
		require.NotNil(t, position)
		hash := sha256.Sum256(want)
		require.Equal(t, hash[:], position.Hash)
		versions, _, err := exe.Lib().ListFileVersions(ctx, item.Data.Expected.FileId, 0, 10)
		require.NoError(t, err)
		require.Len(t, versions, 1)
		require.Equal(t, hash[:], versions[0].Hash)
		file, err := exe.Lib().GetByPath(ctx, 0, "Unforged/Archive/"+item.TargetPath)
		require.NoError(t, err)
		require.NotNil(t, file)
		require.Equal(t, item.Data.Expected.FileId, file.ID)
	}
}
